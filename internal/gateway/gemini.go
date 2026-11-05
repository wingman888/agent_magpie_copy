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

// This file serves Gemini's generateContent API as one more client protocol.
// Calls are translated to the catalog provider's own protocol and back, the
// same way the other client APIs are.

// geminiFrom labels Gemini calls in the call log.
const geminiFrom provider.Protocol = "gemini"

const geminiPrefix = "/v1beta/models/"

// geminiMethods are the methods the models list advertises.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// geminiStatus names an HTTP status the way Gemini's error bodies do.
func geminiStatus(code int) string {
	switch {
	case code == 400:
		return "INVALID_ARGUMENT"
	case code == 401:
		return "UNAUTHENTICATED"
	case code == 403:
		return "PERMISSION_DENIED"
	case code == 404:
		return "NOT_FOUND"
	case code == 429:
		return "RESOURCE_EXHAUSTED"
	case code == 502 || code == 503 || code == 529:
		return "UNAVAILABLE"
	case code == 504:
		return "DEADLINE_EXCEEDED"
	case code >= 500:
		return "INTERNAL"
	}
	return "UNKNOWN"
}

// writeGeminiJSON answers with compact JSON.
func writeGeminiJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// writeGeminiError answers in Gemini's error shape.
func writeGeminiError(w http.ResponseWriter, code int, msg string) int {
	writeGeminiJSON(w, code, map[string]any{"error": map[string]any{"code": code, "message": msg, "status": geminiStatus(code)}})
	return code
}

func unknownModelMsg(model string) string {
	return fmt.Sprintf("models/%s is not found; dial knows no model %q", model, model)
}

// geminiModels lists the catalog as Gemini's models list.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		name := e.Name
		if name == "" {
			name = e.ID
		}
		models = append(models, map[string]any{"name": "models/" + e.ID, "displayName": name,
			"supportedGenerationMethods": geminiMethods})
	}
	writeGeminiJSON(w, http.StatusOK, map[string]any{"models": models})
}

// gemini serves POST /v1beta/models/{model}:{method}. The model reference may
// itself contain slashes and colons (ollama tags), so the method is whatever
// follows the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, geminiPrefix)
	i := strings.LastIndex(rest, ":")
	if !ok || i <= 0 {
		writeGeminiError(w, http.StatusNotFound, "dial serves POST /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	model, method := rest[:i], rest[i+1:]
	if method != "generateContent" && method != "streamGenerateContent" && method != "countTokens" {
		writeGeminiError(w, http.StatusNotFound, fmt.Sprintf("dial has no method %q; it serves generateContent, streamGenerateContent and countTokens", method))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if method == "countTokens" {
		s.geminiCount(w, model, body)
		return
	}

	start := time.Now()
	call := Call{Time: start, From: geminiFrom, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, vendorModel, ok := provider.Resolve(model)
	if !ok {
		call.Status, call.Error = http.StatusNotFound, "unknown model"
		s.record(call)
		writeGeminiError(w, http.StatusNotFound, unknownModelMsg(model))
		return
	}
	call.Provider = p.ID
	req, err := parseGemini(body)
	if err != nil {
		call.Status, call.Error = http.StatusBadRequest, err.Error()
		s.record(call)
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	to := p.Speaks()
	if len(to) == 0 {
		call.Status, call.Error = http.StatusBadGateway, p.Name+" has no endpoint configured"
		s.record(call)
		writeGeminiError(w, http.StatusBadGateway, call.Error)
		return
	}
	call.To = to[0]
	stream := method == "streamGenerateContent"
	call.Status, call.Error = s.geminiTranslate(w, r, p, to[0], vendorModel, req, stream, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: vendorModel,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiCount estimates the tokens of a request; no upstream is asked.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeGeminiError(w, http.StatusNotFound, unknownModelMsg(model))
		return
	}
	req, err := parseGemini(body)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeGeminiJSON(w, http.StatusOK, map[string]any{"totalTokens": estimateTokens(req)})
}

// geminiTranslate sends a translated request to the provider and writes the
// reply in Gemini's shape. The provider is always streamed, as in translate.
func (s *Server) geminiTranslate(w http.ResponseWriter, r *http.Request, p provider.Provider, to provider.Protocol, model string, req *Request, stream bool, u *Usage) (int, string) {
	req.Stream = true
	res, err := s.forward(r.Context(), p, to, pathOf(to), build(to, req, model, p.Host()), r.Header)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeGeminiError(w, http.StatusBadGateway, msg), msg
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeGeminiError(w, res.StatusCode, msg), msg
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeGeminiError(w, http.StatusBadGateway, msg), msg
	}
	dec := decoder(to)
	if stream {
		return s.geminiStream(w, res.Body, dec, model, u, p)
	}
	var col collector
	readSSE(res.Body, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		msg := p.Name + ": " + col.err
		return writeGeminiError(w, http.StatusBadGateway, msg), msg
	}
	res2 := col.finish()
	u.add(res2.Usage)
	writeGeminiJSON(w, http.StatusOK, geminiReply(res2, model))
	return http.StatusOK, col.err
}

// geminiStream relays a provider stream as Gemini stream events: one event
// per part, in the order the provider produced them.
func (s *Server) geminiStream(w http.ResponseWriter, body io.Reader, dec func(string, func(Event)) error, model string, u *Usage, p provider.Provider) (int, string) {
	sw := newSSEWriter(w)
	g := &geminiStreamer{w: sw}
	var failed string
	readSSE(body, func(_, data string) error {
		return dec(data, func(ev Event) {
			if ev.Kind == KError {
				failed = ev.Text
				return
			}
			g.event(ev)
		})
	})
	u.add(g.usage)
	if failed != "" {
		if !sw.begun {
			msg := p.Name + ": " + failed
			return writeGeminiError(w, http.StatusBadGateway, msg), failed
		}
		g.fail(p.Name + ": " + failed)
		return http.StatusOK, failed
	}
	g.finish(model)
	return http.StatusOK, ""
}

// geminiStreamer turns provider events into Gemini stream events. Text and
// thinking go out as they arrive; a function call goes out once its
// arguments are complete.
type geminiStreamer struct {
	w     *sseWriter
	id    string
	tool  *Part // the call whose arguments are still arriving
	args  strings.Builder
	stop  string
	usage Usage
	n     int
}

func (g *geminiStreamer) event(ev Event) {
	switch ev.Kind {
	case KStart:
		g.id = ev.MsgID
		g.usage.add(ev.Usage)
	case KUsage:
		g.usage.add(ev.Usage)
	case KText:
		g.closeTool()
		if ev.Text != "" {
			g.send(map[string]any{"text": ev.Text})
		}
	case KThink:
		g.closeTool()
		if ev.Text != "" {
			g.send(map[string]any{"text": ev.Text, "thought": true})
		}
	case KSig:
		g.closeTool()
		if ev.Text != "" {
			g.send(map[string]any{"thought": true, "thoughtSignature": ev.Text})
		}
	case KToolStart:
		g.closeTool()
		id := ev.ID
		if id == "" {
			g.n++
			id = fmt.Sprintf("call_%d_%s", g.n, ev.Name)
		}
		g.tool = &Part{ID: id, Name: ev.Name}
	case KToolArgs:
		if g.tool != nil {
			g.args.WriteString(ev.Text)
		}
	case KStop:
		g.closeTool()
		g.stop = ev.Stop
	}
}

func (g *geminiStreamer) closeTool() {
	if g.tool == nil {
		return
	}
	g.send(map[string]any{"functionCall": map[string]any{
		"id": g.tool.ID, "name": g.tool.Name, "args": parseArgs(g.args.String())}})
	g.tool = nil
	g.args.Reset()
}

// send writes one part as its own event.
func (g *geminiStreamer) send(part map[string]any) {
	g.w.event("", map[string]any{"candidates": []any{map[string]any{
		"content": map[string]any{"role": "model", "parts": []any{part}}}}})
}

// finish ends the stream with the finish reason and the usage.
func (g *geminiStreamer) finish(model string) {
	g.closeTool()
	id := g.id
	if id == "" {
		id = newID()
	}
	g.w.event("", map[string]any{"candidates": []any{map[string]any{
		"content":      map[string]any{"role": "model", "parts": []any{}},
		"finishReason": geminiFinish(g.stop)}},
		"usageMetadata": geminiUsage(g.usage),
		"modelVersion":  model,
		"responseId":    id,
	})
}

// fail ends a stream that broke part way with an error event.
func (g *geminiStreamer) fail(msg string) {
	g.w.event("", map[string]any{"error": map[string]any{"code": http.StatusBadGateway, "message": msg,
		"status": geminiStatus(http.StatusBadGateway)}})
}

// geminiReply is a whole reply in Gemini's shape.
func geminiReply(res Result, model string) map[string]any {
	parts := []any{}
	for i, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text == "" && p.Signature == "" {
				continue
			}
			part := map[string]any{"text": p.Text, "thought": true}
			if p.Signature != "" {
				part["thoughtSignature"] = p.Signature
			}
			parts = append(parts, part)
		case ToolCall:
			id := p.ID
			if id == "" {
				id = fmt.Sprintf("call_%d_%s", i, p.Name)
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{
				"id": id, "name": p.Name, "args": argsOf(p)}})
		}
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	return map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(res.Stop)}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	}
}

// geminiFinish maps the gateway's stop reasons to Gemini's. A tool call
// finishes with STOP, as Gemini does.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage reports the prompt, the candidates and the thoughts. The
// prompt count includes cached tokens, as the upstream's does.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	return map[string]any{
		"promptTokenCount":     prompt,
		"candidatesTokenCount": u.Output,
		"thoughtsTokenCount":   u.Reasoning,
		"totalTokenCount":      prompt + u.Output,
	}
}
