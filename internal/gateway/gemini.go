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

// geminiProto names the Gemini API in the call log. It is never a provider
// protocol: nothing speaks Gemini upstream.
const geminiProto provider.Protocol = "gemini"

// geminiPrefix is where the Gemini API lives; {model} and the method follow.
const geminiPrefix = "/v1beta/models/"

// geminiModels answers GET /v1beta/models with the catalog.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, e := range provider.Catalog() {
		data = append(data, map[string]any{
			"name":                       "models/" + e.ID,
			"displayName":                e.Name,
			"description":                e.Name + " via dial",
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		})
	}
	writeGeminiJSON(w, http.StatusOK, map[string]any{"models": data})
}

// geminiModel routes POST /v1beta/models/{model}:{method}. The model is a
// catalog reference such as provider/model, so it can contain slashes and
// colons; the method is whatever follows the last colon.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, geminiPrefix)
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeGeminiError(w, http.StatusNotFound, "dial serves the Gemini methods generateContent, streamGenerateContent and countTokens")
		return
	}
	model, method := rest[:i], rest[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch method {
	case "generateContent":
		s.geminiGenerate(w, r, model, body, false, false)
	case "streamGenerateContent":
		// alt=sse asks for an event stream; without it Gemini sends one JSON array
		s.geminiGenerate(w, r, model, body, true, r.URL.Query().Get("alt") == "sse")
	case "countTokens":
		s.geminiCount(w, model, body)
	default:
		writeGeminiError(w, http.StatusNotFound, fmt.Sprintf("dial has no Gemini method %q", method))
	}
}

// geminiCount estimates the tokens of a request. The request may be the
// generate body itself or wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeGeminiError(w, http.StatusNotFound, unknownModelMsg(model))
		return
	}
	raw := body
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 {
		raw = wrap.GenerateContentRequest
	}
	req, err := parseGemini(raw)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeGeminiJSON(w, http.StatusOK, map[string]any{"totalTokens": estimateTokens(req)})
}

// geminiGenerate serves generateContent and streamGenerateContent. The
// upstream is always streamed, and the reply is then gathered or relayed as
// the Gemini client asked.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, ref string, body []byte, streaming, sse bool) {
	start := time.Now()
	call := Call{Time: start, From: geminiProto, Model: ref, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(ref)
	if !ok {
		call.Status, call.Error = http.StatusNotFound, "unknown model"
		s.record(call)
		writeGeminiError(w, http.StatusNotFound, unknownModelMsg(ref))
		return
	}
	call.Provider = p.ID
	if p.IsAccount() {
		ap, err := provider.Prepare(r.Context(), p)
		if err != nil {
			call.Status, call.Error = writeGeminiError(w, accountStatus(err), err.Error()), err.Error()
			call.Millis = time.Since(start).Milliseconds()
			s.record(call)
			return
		}
		p = ap
	}
	to := p.Speaks()
	if len(to) == 0 {
		writeGeminiError(w, http.StatusBadGateway, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	req, err := parseGemini(body)
	if err != nil {
		call.Status, call.Error = writeGeminiError(w, http.StatusBadRequest, err.Error()), err.Error()
		s.record(call)
		return
	}
	call.Status, call.Error = s.geminiTranslate(w, r, p, to[0], model, ref, req, streaming, sse, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiTranslate sends a Gemini request to a provider speaking to, and writes
// the reply in Gemini's shape. ref is the catalog name the client used;
// model is the vendor's own name.
func (s *Server) geminiTranslate(w http.ResponseWriter, r *http.Request, p provider.Provider, to provider.Protocol, model, ref string, req *Request, streaming, sse bool, u *Usage) (int, string) {
	req.Stream = true
	res, err := s.upstream(r.Context(), p, to, build(to, req, model, p.Host()), r.Header)
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
	src, isStream := eventBody(p, res)
	if !isStream {
		b, _ := io.ReadAll(io.LimitReader(src, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeGeminiError(w, http.StatusBadGateway, msg), msg
	}
	dec := decoder(to)
	if streaming {
		enc := &geminiStream{w: w, sse: sse, ref: ref, sw: newSSEWriter(w)}
		var failed string
		readSSE(src, func(_, data string) error {
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
		if failed != "" {
			if !enc.sent() {
				return writeGeminiError(w, http.StatusBadGateway, p.Name+": "+failed), failed
			}
			enc.fail(p.Name + ": " + failed)
			return http.StatusOK, failed
		}
		enc.finish()
		return http.StatusOK, ""
	}
	var col collector
	readSSE(src, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		msg := p.Name + ": " + col.err
		return writeGeminiError(w, http.StatusBadGateway, msg), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	out := renderGemini(res2, ref)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(out)
	return http.StatusOK, col.err
}

// geminiStream writes Gemini stream chunks. Each part is its own event, with
// the finish reason and usage on the last one. A reply that is not asked for
// as SSE (no alt=sse) is buffered and sent as one JSON array.
type geminiStream struct {
	w     http.ResponseWriter
	sse   bool
	ref   string
	sw    *sseWriter
	items [][]byte
	id    string
	usage Usage
	stop  string
	tool  *Part // a function call still gathering its arguments
	args  strings.Builder
}

func (e *geminiStream) sent() bool {
	return e.sse && e.sw.begun
}

func (e *geminiStream) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
		e.usage.add(ev.Usage)
	case KUsage:
		e.usage.add(ev.Usage)
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.part(geminiPart(Part{Kind: Text, Text: ev.Text}))
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.part(geminiPart(Part{Kind: Thinking, Text: ev.Text}))
		}
	case KToolStart:
		e.flushTool()
		e.tool = &Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
		e.args.Reset()
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushTool()
		e.stop = ev.Stop
	}
}

// flushTool sends the open function call once its arguments are complete.
func (e *geminiStream) flushTool() {
	if e.tool == nil {
		return
	}
	p := *e.tool
	p.Args = parseArgs(e.args.String())
	e.tool = nil
	e.args.Reset()
	e.part(geminiPart(p))
}

func (e *geminiStream) part(p map[string]any) {
	e.emit(e.chunk([]any{p}, "", false))
}

func (e *geminiStream) finish() {
	e.flushTool()
	e.emit(e.chunk([]any{}, finishReason(e.stop), true))
	if !e.sse {
		e.writeArray()
	}
}

// fail ends a stream that broke off after it had started.
func (e *geminiStream) fail(msg string) {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": http.StatusBadGateway, "message": msg, "status": geminiStatusName(http.StatusBadGateway)}})
	e.sw.event("", json.RawMessage(b))
}

func (e *geminiStream) chunk(parts []any, finish string, withUsage bool) []byte {
	if e.id == "" {
		e.id = newID()
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	c := map[string]any{"candidates": []any{cand}, "modelVersion": e.ref, "responseId": e.id}
	if withUsage {
		c["usageMetadata"] = usageMeta(e.usage)
	}
	b, _ := json.Marshal(c)
	return b
}

func (e *geminiStream) emit(b []byte) {
	if e.sse {
		e.sw.event("", json.RawMessage(b))
		return
	}
	e.items = append(e.items, b)
}

func (e *geminiStream) writeArray() {
	var out []byte
	out = append(out, '[')
	for i, b := range e.items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, b...)
	}
	out = append(out, ']')
	e.w.Header().Set("Content-Type", "application/json")
	e.w.WriteHeader(http.StatusOK)
	e.w.Write(out)
}

// renderGemini is a whole reply in Gemini's shape.
func renderGemini(res Result, ref string) []byte {
	parts := []any{}
	for _, p := range res.Parts {
		if p.Kind == Text && p.Text == "" {
			continue
		}
		parts = append(parts, geminiPart(p))
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	out := map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": finishReason(res.Stop),
			"index":        0,
		}},
		"usageMetadata": usageMeta(res.Usage),
		"modelVersion":  ref,
		"responseId":    id,
	}
	b, _ := json.Marshal(out)
	return b
}

// geminiPart is one part of a reply.
func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Thinking:
		m := map[string]any{"text": p.Text, "thought": true}
		if p.Signature != "" {
			m["thoughtSignature"] = p.Signature
		}
		return m
	case ToolCall:
		id := p.ID
		if id == "" {
			id = "call_" + newID()
		}
		args := argsOf(p)
		if !json.Valid(args) {
			args = parseArgs(string(args))
		}
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": args}}
	}
	return map[string]any{"text": p.Text}
}

func finishReason(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// usageMeta is the usage block in Gemini's names.
func usageMeta(u Usage) map[string]any {
	return map[string]any{
		"promptTokenCount":     u.Input,
		"candidatesTokenCount": u.Output,
		"thoughtsTokenCount":   u.Reasoning,
		"totalTokenCount":      u.Input + u.Output,
	}
}

// unknownModelMsg says which models dial does know, for a 404.
func unknownModelMsg(model string) string {
	msg := fmt.Sprintf("dial knows no model %q", model)
	if ids := provider.IDs(); len(ids) > 0 {
		msg += "; it has " + strings.Join(ids, ", ")
	} else {
		msg += "; add a provider in dial first"
	}
	return msg
}

// accountStatus is the HTTP status for a failed account login.
func accountStatus(err error) int {
	if err == provider.ErrCodexLoggedOut || err == provider.ErrCopilotRelogin {
		return http.StatusUnauthorized
	}
	return http.StatusBadGateway
}

// writeGeminiError answers in Gemini's error shape and returns the status.
func writeGeminiError(w http.ResponseWriter, code int, msg string) int {
	writeGeminiJSON(w, code, map[string]any{"error": map[string]any{
		"code": code, "message": msg, "status": geminiStatusName(code),
	}})
	return code
}

func writeGeminiJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

func geminiStatusName(code int) string {
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
	case 500:
		return "INTERNAL"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	if code >= 500 {
		return "UNAVAILABLE"
	}
	return "UNKNOWN"
}
