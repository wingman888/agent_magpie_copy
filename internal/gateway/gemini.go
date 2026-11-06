package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// geminiFrom labels calls that arrived through the Gemini API. It is a client
// protocol only; no provider speaks it upstream.
const geminiFrom provider.Protocol = "gemini"

// geminiAPIs lists the Gemini routes, for the root description.
var geminiAPIs = []string{"/v1beta/models", "/v1beta/models/{model}:generateContent",
	"/v1beta/models/{model}:streamGenerateContent", "/v1beta/models/{model}:countTokens"}

// geminiModels serves POST /v1beta/models/{model}:{method}. The model is a
// catalog reference, which may itself contain colons; the method is what
// follows the last one.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("model")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeGeminiError(w, http.StatusNotFound, "dial serves the Gemini API under /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	ref, method := rest[:i], rest[i+1:]
	switch method {
	case "generateContent":
		s.geminiCall(w, r, ref, false)
	case "streamGenerateContent":
		s.geminiCall(w, r, ref, true)
	case "countTokens":
		s.geminiCount(w, r, ref)
	default:
		writeGeminiError(w, http.StatusNotFound, fmt.Sprintf("dial has no Gemini method %q for model %s", method, ref))
	}
}

// geminiList serves GET /v1beta/models: every model in the catalog.
func (s *Server) geminiList(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		name := e.Name
		if name == "" {
			name = e.ID
		}
		models = append(models, map[string]any{
			"name":                       "models/" + e.ID,
			"displayName":                name,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		})
	}
	b, _ := json.Marshal(map[string]any{"models": models})
	writeGeminiRaw(w, http.StatusOK, b)
}

// geminiCall answers generateContent and streamGenerateContent.
func (s *Server) geminiCall(w http.ResponseWriter, r *http.Request, ref string, stream bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	start := time.Now()
	call := Call{Time: start, From: geminiFrom, Model: ref, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(ref)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeGeminiError(w, http.StatusNotFound, geminiUnknownModelMsg(ref))
		return
	}
	call.Provider = p.ID
	req, err := parseGemini(body, ref)
	if err != nil {
		call.Status, call.Error = 400, err.Error()
		s.record(call)
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Account != "" {
		// a signed-in agent account: take its current token (and base) for this request
		sess, err := provider.Session(r.Context(), p)
		if err != nil {
			call.Status, call.Error = 401, err.Error()
			s.record(call)
			writeGeminiError(w, http.StatusUnauthorized, err.Error())
			return
		}
		p = sess
	}
	to := p.Speaks()
	if len(to) == 0 {
		call.Status, call.Error = 502, p.Name+" has no endpoint configured"
		s.record(call)
		writeGeminiError(w, http.StatusBadGateway, call.Error)
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.geminiReply(w, r, p, to[0], model, ref, req, stream, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiReply sends the request upstream as a stream, whatever the client
// asked for, and answers the client in Gemini's shape.
func (s *Server) geminiReply(w http.ResponseWriter, r *http.Request, p provider.Provider, to provider.Protocol,
	model, ref string, req *Request, stream bool, u *Usage) (int, string) {
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
	in, ok := geminiSSEBody(res)
	if !ok {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeGeminiError(w, http.StatusBadGateway, msg), msg
	}
	dec := decoder(to)
	out := &geminiOut{w: newSSEWriter(w), stream: stream}
	readSSE(in, func(_, data string) error {
		return dec(data, out.event)
	})
	res2 := out.col.finish()
	u.add(res2.Usage)
	failed := out.col.err != ""
	if failed && len(res2.Parts) == 0 && !out.w.begun {
		msg := p.Name + ": " + out.col.err
		return writeGeminiError(w, http.StatusBadGateway, msg), out.col.err
	}
	if stream {
		for ; out.sent < len(res2.Parts); out.sent++ {
			out.part(res2.Parts[out.sent])
		}
		b, _ := json.Marshal(geminiEnvelope(res2, ref, nil, failed))
		out.w.event("", json.RawMessage(b))
		return 200, out.col.err
	}
	writeGeminiRaw(w, http.StatusOK, renderGemini(res2, ref, failed))
	return 200, out.col.err
}

// geminiCount answers countTokens with a rough estimate. The body is either a
// generation request or one wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, ref string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	json.Unmarshal(body, &wrap)
	if len(wrap.GenerateContentRequest) > 0 {
		body = wrap.GenerateContentRequest
	}
	req, err := parseGemini(body, ref)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(ref); !ok {
		writeGeminiError(w, http.StatusNotFound, geminiUnknownModelMsg(ref))
		return
	}
	writeGeminiRaw(w, http.StatusOK, []byte(fmt.Sprintf(`{"totalTokens":%d}`, geminiEstimateTokens(req))))
}

// geminiUnknownModelMsg explains a model the gateway does not have.
func geminiUnknownModelMsg(ref string) string {
	msg := fmt.Sprintf("dial knows no model %q", ref)
	if ids := provider.IDs(); len(ids) > 0 {
		msg += "; it has " + strings.Join(ids, ", ")
	} else {
		msg += "; add a provider in dial first"
	}
	return msg
}

// geminiSSEBody reports whether an upstream reply is an event stream and returns a
// reader over it. The Content-Type is trusted when present; without one the
// body is looked at, since some upstreams (ChatGPT's backend) omit it.
func geminiSSEBody(res *http.Response) (io.Reader, bool) {
	br := bufio.NewReader(res.Body)
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		return br, true
	}
	head, _ := br.Peek(6)
	s := strings.TrimLeft(string(head), " \r\n\t")
	return br, strings.HasPrefix(s, "data:") || strings.HasPrefix(s, "event:") || strings.HasPrefix(s, ":")
}

// geminiStatus is the status word Gemini uses for an HTTP status.
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

// writeGeminiError answers in Gemini's error shape.
func writeGeminiError(w http.ResponseWriter, status int, msg string) int {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}})
	writeGeminiRaw(w, status, b)
	return status
}

// writeGeminiRaw writes a compact JSON body.
func writeGeminiRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}
