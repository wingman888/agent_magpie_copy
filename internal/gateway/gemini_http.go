package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// geminiProto labels Gemini calls in the call log. Gemini is a client
// protocol only, so it is not one of the provider.Protocol upstreams speak.
const geminiProto = provider.Protocol("gemini")

// geminiMethods are the methods every catalog model supports.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// gemini routes POST /v1beta/models/{rest}. The rest is a catalog reference
// followed by ":method". References contain slashes (provider/model) and may
// contain colons (ollama tags), so the method is whatever follows the last
// colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeGeminiError(w, http.StatusNotFound, "expected /v1beta/models/{model}:{method}, got "+rest)
		return
	}
	ref, method := rest[:i], rest[i+1:]
	switch method {
	case "generateContent":
		s.geminiGenerate(w, r, ref, false)
	case "streamGenerateContent":
		// always SSE, whatever ?alt says
		s.geminiGenerate(w, r, ref, true)
	case "countTokens":
		s.geminiCount(w, r, ref)
	default:
		writeGeminiError(w, http.StatusNotFound, fmt.Sprintf("%s is not a supported method for %s", method, ref))
	}
}

// geminiGenerate serves generateContent and streamGenerateContent by
// translating the Gemini request to whichever protocol the model's provider
// speaks.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, ref string, stream bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	start := time.Now()
	call := Call{Time: start, From: geminiProto, Model: ref, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(ref)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeGeminiError(w, http.StatusNotFound, unknownModelMsg(ref))
		return
	}
	call.Provider = p.ID
	to := p.Speaks()
	if len(to) == 0 {
		writeGeminiError(w, http.StatusBadGateway, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.geminiTranslate(w, r, p, to[0], ref, model, stream, body, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.logCall(call, start, p, model)
}

// geminiTranslate speaks to the provider in its protocol (always streamed,
// as translate does) and answers the Gemini client in Gemini's format.
// ref is the client's model reference, model the provider's own id.
func (s *Server) geminiTranslate(w http.ResponseWriter, r *http.Request, p provider.Provider, to provider.Protocol, ref, model string, stream bool, body []byte, u *Usage) (int, string) {
	req, err := parseGemini(ref, stream, body)
	if err != nil {
		return writeGeminiError(w, http.StatusBadRequest, err.Error()), err.Error()
	}
	req.Stream = true
	res, err := s.forward(r.Context(), p, to, pathOf(to), build(to, req, model, p.Host()), r.Header)
	if err != nil {
		return writeGeminiError(w, http.StatusBadGateway, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeGeminiError(w, res.StatusCode, msg), msg
	}
	dec := decoder(to)
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeGeminiError(w, http.StatusBadGateway, msg), msg
	}
	if stream {
		enc := &geminiEncoder{w: newSSEWriter(w), model: ref}
		var failed string
		readSSE(res.Body, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		enc.finish()
		return 200, failed
	}
	var col collector
	readSSE(res.Body, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeGeminiError(w, http.StatusBadGateway, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(renderGemini(res2, ref))
	return http.StatusOK, col.err
}

// geminiCount answers countTokens with a rough estimate, as the Anthropic
// count_tokens route does. The body may be the request itself or wrap it in
// generateContentRequest. Like count_tokens, it is not logged as a call.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, ref string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(ref); !ok {
		writeGeminiError(w, http.StatusNotFound, unknownModelMsg(ref))
		return
	}
	req, err := parseGemini(ref, false, geminiCountBody(body))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, _ := json.Marshal(map[string]int{"totalTokens": geminiEstimate(req)})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
}

// geminiEstimate counts characters of a request and divides by four.
func geminiEstimate(r *Request) int {
	n := len(r.System)
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range r.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return n / 4
}

// geminiModels lists the catalog in Gemini's model list format.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		name := e.Name
		if name == "" {
			name = e.ID
		}
		models = append(models, map[string]any{
			"name":                       "models/" + e.ID,
			"displayName":                name,
			"supportedGenerationMethods": geminiMethods,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// writeGeminiError answers in Gemini's error shape, compact JSON.
func writeGeminiError(w http.ResponseWriter, status int, msg string) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(renderGeminiError(status, geminiStatus(status), msg))
	return status
}

// geminiStatus is the gRPC-style status name Gemini puts in error bodies.
func geminiStatus(code int) string {
	switch code {
	case 400:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 502, 503, 529:
		return "UNAVAILABLE"
	}
	if code >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}
