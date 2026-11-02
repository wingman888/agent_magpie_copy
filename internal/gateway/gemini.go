package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// geminiAPI is the Gemini generateContent API. It is only ever a client
// protocol: dial never speaks it to a provider, so it is not in
// provider.Protocols.
const geminiAPI provider.Protocol = "gemini"

// ---- routes -------------------------------------------------------------------

// geminiModels answers GET /v1beta/models.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	list := []map[string]any{}
	for _, e := range provider.Catalog() {
		list = append(list, geminiModel(e))
	}
	writeGeminiJSON(w, 200, map[string]any{"models": list})
}

func geminiModel(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "displayName": name, "description": name + " via dial",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}

// geminiGet answers GET /v1beta/models/{id}.
func (s *Server) geminiGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("rest"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeGeminiJSON(w, 200, geminiModel(e))
			return
		}
	}
	writeError(w, geminiAPI, 404, "unknown model "+id)
}

// geminiPost routes models/{model}:{method}. The model reference can hold
// slashes and colons, so the method is whatever follows the last colon.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, geminiAPI, 404, "expected models/{model}:generateContent, :streamGenerateContent or :countTokens")
		return
	}
	model, method := rest[:i], rest[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiAPI, 400, err.Error())
		return
	}
	switch method {
	case "generateContent":
		s.geminiGenerate(w, r, model, body, false, true)
	case "streamGenerateContent":
		s.geminiGenerate(w, r, model, body, true, r.URL.Query().Get("alt") == "sse")
	case "countTokens":
		s.geminiCount(w, body)
	default:
		writeError(w, geminiAPI, 404, "unknown method "+method)
	}
}

// geminiBody folds the model and stream flag from the URL into the request
// body (Gemini carries neither there), unwrapping generateContentRequest.
func geminiBody(body []byte, model string, stream bool) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return body
	}
	if inner, ok := m["generateContentRequest"]; ok {
		var im map[string]json.RawMessage
		if json.Unmarshal(inner, &im) == nil {
			m = im
		}
	}
	mb, _ := json.Marshal(model)
	m["model"] = mb
	if stream {
		m["stream"] = json.RawMessage("true")
	} else {
		delete(m, "stream")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// bufferedWriter captures a reply so it can be re-wrapped.
type bufferedWriter struct {
	h    http.Header
	code int
	buf  bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header         { return b.h }
func (b *bufferedWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufferedWriter) WriteHeader(c int)           { b.code = c }

func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, ref string, body []byte, stream, sseOut bool) {
	if strings.HasPrefix(ref, "models/") {
		if _, _, ok := provider.Resolve(ref); !ok {
			ref = strings.TrimPrefix(ref, "models/")
		}
	}
	start := time.Now()
	call := Call{Time: start, From: geminiAPI, Model: ref, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(ref)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		msg := fmt.Sprintf("dial knows no model %q", ref)
		if ids := provider.IDs(); len(ids) > 0 {
			msg += "; it has " + strings.Join(ids, ", ")
		} else {
			msg += "; add a provider in dial first"
		}
		writeError(w, geminiAPI, 404, msg)
		return
	}
	call.Provider = p.ID
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, geminiAPI, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	in := geminiBody(body, ref, stream && sseOut)
	// a streaming call without alt=sse wants a JSON array: run it whole
	// and wrap the one chunk
	if stream && !sseOut {
		bw := &bufferedWriter{h: http.Header{}, code: 200}
		call.Status, call.Error = s.translate(bw, r, p, geminiAPI, to[0], model, in, &call.Usage)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(bw.code)
		if bw.code == 200 {
			w.Write([]byte("["))
			w.Write(bytes.TrimSpace(bw.buf.Bytes()))
			w.Write([]byte("]"))
		} else {
			w.Write(bw.buf.Bytes())
		}
	} else {
		call.Status, call.Error = s.translate(w, r, p, geminiAPI, to[0], model, in, &call.Usage)
	}
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiCount estimates tokens; Gemini's own counter is not reachable.
func (s *Server) geminiCount(w http.ResponseWriter, body []byte) {
	req, err := parseGemini(geminiBody(body, "", false))
	if err != nil {
		writeError(w, geminiAPI, 400, err.Error())
		return
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
			if p.Kind == Image {
				n += 1000
			}
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	t := n / 4
	if t == 0 && n > 0 {
		t = 1
	}
	writeGeminiJSON(w, 200, map[string]any{"totalTokens": t})
}

// ---- request ------------------------------------------------------------------

type gPart struct {
	Text       *string `json:"text"`
	Thought    bool    `json:"thought"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData"`
	FileData *struct {
		MimeType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData"`
	FunctionCall *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gRequest struct {
	Model             string          `json:"model"`
	Stream            bool            `json:"stream"`
	Contents          []gContent      `json:"contents"`
	SystemInstruction json.RawMessage `json:"systemInstruction"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name                 string          `json:"name"`
			Description          string          `json:"description"`
			Parameters           json.RawMessage `json:"parameters"`
			ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
		} `json:"functionDeclarations"`
	} `json:"tools"`
	ToolConfig struct {
		FunctionCallingConfig struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig struct {
		MaxOutputTokens    int             `json:"maxOutputTokens"`
		Temperature        *float64        `json:"temperature"`
		TopP               *float64        `json:"topP"`
		StopSequences      []string        `json:"stopSequences"`
		ResponseMimeType   string          `json:"responseMimeType"`
		ResponseSchema     json.RawMessage `json:"responseSchema"`
		ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"`
		ThinkingConfig     *struct {
			ThinkingBudget  *int   `json:"thinkingBudget"`
			ThinkingLevel   string `json:"thinkingLevel"`
			IncludeThoughts bool   `json:"includeThoughts"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

// lowerTypes rewrites Gemini's upper-case schema types (OBJECT, STRING…) to
// the lower-case ones everyone else wants, at any depth.
func lowerTypes(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, e := range t {
				if k == "type" {
					switch tv := e.(type) {
					case string:
						t[k] = strings.ToLower(tv)
						continue
					case []any:
						for i, it := range tv {
							if s, ok := it.(string); ok {
								tv[i] = strings.ToLower(s)
							}
						}
						continue
					}
				}
				t[k] = walk(e)
			}
		case []any:
			for i, e := range t {
				t[i] = walk(e)
			}
		}
		return x
	}
	out, err := json.Marshal(walk(v))
	if err != nil {
		return raw
	}
	return out
}

// instructionText reads systemInstruction: a content, or a bare string.
func instructionText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var c gContent
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range c.Parts {
		if p.Text != nil {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}

func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	gc := g.GenerationConfig
	r := &Request{Model: g.Model, Stream: g.Stream, MaxTokens: gc.MaxOutputTokens, Temp: gc.Temperature, TopP: gc.TopP,
		Stop: gc.StopSequences, System: instructionText(g.SystemInstruction)}

	// JSON output: the upstreams have no such field, so ask in the prompt
	if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") {
		schema := gc.ResponseJSONSchema
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = gc.ResponseSchema
		}
		ins := "Respond with only valid JSON, with no markdown fences or commentary."
		if schema = lowerTypes(schema); len(schema) > 0 {
			ins = "Respond with only valid JSON that conforms to this JSON schema, with no markdown fences or commentary: " + string(schema)
		}
		if r.System != "" {
			r.System += "\n\n"
		}
		r.System += ins
	}

	if tc := gc.ThinkingConfig; tc != nil {
		switch lv := strings.ToLower(tc.ThinkingLevel); {
		case lv == "high":
			r.Effort = "high"
		case lv == "medium":
			r.Effort = "medium"
		case lv == "low" || lv == "minimal":
			r.Effort = "low"
		case tc.ThinkingBudget != nil:
			switch b := *tc.ThinkingBudget; {
			case b < 0:
				r.Effort = "medium"
			case b == 0:
				r.Effort = ""
			case b <= 4096:
				r.Effort = "low"
			case b <= 16384:
				r.Effort = "medium"
			default:
				r.Effort = "high"
			}
		}
		r.Thinking = r.Effort != "" || tc.IncludeThoughts
	}

	for _, t := range g.Tools {
		for _, d := range t.FunctionDeclarations {
			schema := d.Parameters
			if len(bytes.TrimSpace(schema)) == 0 {
				schema = d.ParametersJSONSchema
			}
			r.Tools = append(r.Tools, Tool{Name: d.Name, Description: d.Description, Schema: lowerTypes(schema)})
		}
	}
	fc := g.ToolConfig.FunctionCallingConfig
	switch strings.ToUpper(fc.Mode) {
	case "NONE":
		r.ToolChoice = "none"
	case "AUTO":
		r.ToolChoice = "auto"
	case "ANY":
		r.ToolChoice = "required"
		if len(fc.AllowedFunctionNames) == 1 {
			r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
		}
	}

	// A response without an id belongs to the latest call of that name.
	pending := map[string][]string{} // unanswered calls of the latest model turn
	latest := map[string]string{}    // the latest call of each name
	for _, c := range g.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		msg := Message{Role: role}
		seen := map[string]bool{}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				fcall := p.FunctionCall
				id := fcall.ID
				if id == "" {
					id = fmt.Sprintf("call_%s_%d_%d", newID(), len(r.Messages), len(msg.Parts))
				}
				if !seen[fcall.Name] {
					seen[fcall.Name] = true
					pending[fcall.Name] = nil
				}
				pending[fcall.Name] = append(pending[fcall.Name], id)
				latest[fcall.Name] = id
				args := fcall.Args
				if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
					args = json.RawMessage("{}")
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fcall.Name, Args: args})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					if q := pending[fr.Name]; len(q) > 0 {
						id, pending[fr.Name] = q[0], q[1:]
					} else if l, ok := latest[fr.Name]; ok {
						id = l
					} else {
						id = "call_" + newID()
					}
				} else {
					q := pending[fr.Name][:0:0]
					for _, x := range pending[fr.Name] {
						if x != id {
							q = append(q, x)
						}
					}
					pending[fr.Name] = q
				}
				out := strings.TrimSpace(string(fr.Response))
				if out == "" || out == "null" {
					out = "{}"
				}
				var buf bytes.Buffer
				if json.Compact(&buf, []byte(out)) == nil {
					out = buf.String()
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Name: fr.Name, Text: out})
			case p.InlineData != nil:
				if strings.HasPrefix(strings.ToLower(p.InlineData.MimeType), "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment: %s, %d bytes]", p.InlineData.MimeType, len(p.InlineData.Data)*3/4)})
				}
			case p.FileData != nil:
				mt := strings.ToLower(p.FileData.MimeType)
				if mt == "" || strings.HasPrefix(mt, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file: %s (%s)]", p.FileData.FileURI, p.FileData.MimeType)})
				}
			case p.Text != nil:
				if p.Thought {
					if role == "assistant" && *p.Text != "" {
						msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: *p.Text})
					}
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
				}
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}
	return r, nil
}

// ---- response -----------------------------------------------------------------

func finishOf(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

func (u Usage) gemini() map[string]any {
	in := u.Input + u.CacheRead
	m := map[string]any{"promptTokenCount": in, "candidatesTokenCount": u.Output,
		"thoughtsTokenCount": u.Reasoning, "totalTokenCount": in + u.Output}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

func geminiCall(id, name string, args json.RawMessage) map[string]any {
	if id == "" {
		id = "call_" + newID()
	}
	return map[string]any{"functionCall": map[string]any{"id": id, "name": name, "args": argsOf(Part{Args: args})}}
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Thinking:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text, "thought": true})
			}
		case Text:
			parts = append(parts, map[string]any{"text": p.Text})
		case ToolCall:
			parts = append(parts, geminiCall(p.ID, p.Name, p.Args))
		}
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": parts},
			"finishReason": finishOf(res.Stop), "index": 0}},
		"usageMetadata": res.Usage.gemini(), "modelVersion": model, "responseId": id})
	return b
}

// geminiEncoder writes events as a Gemini SSE stream: every thought, text
// run and function call is a chunk of its own, and a last chunk closes the
// reply with the finish reason and token counts.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector
	tool  *struct {
		id, name string
		args     strings.Builder
	}
}

func (e *geminiEncoder) chunk(parts []map[string]any, extra map[string]any) {
	if e.id == "" {
		e.id = newID()
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	out := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": e.model, "responseId": e.id}
	for k, v := range extra {
		if k == "finishReason" {
			cand[k] = v
		} else {
			out[k] = v
		}
	}
	e.w.event("", out)
}

func (e *geminiEncoder) flushTool() {
	t := e.tool
	if t == nil {
		return
	}
	e.tool = nil
	e.chunk([]map[string]any{geminiCall(t.id, t.name, parseArgs(t.args.String()))}, nil)
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text}}, nil)
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text, "thought": true}}, nil)
		}
	case KToolStart:
		e.flushTool()
		id := ev.ID
		if id == "" {
			id = "call_" + newID()
		}
		e.tool = &struct {
			id, name string
			args     strings.Builder
		}{id: id, name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.tool.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushTool()
	case KError:
		e.flushTool()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": geminiStatus(502)}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	res := e.col.finish()
	e.chunk([]map[string]any{}, map[string]any{"finishReason": finishOf(res.Stop), "usageMetadata": res.Usage.gemini()})
}

// ---- errors -------------------------------------------------------------------

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
	case code == 409:
		return "ABORTED"
	case code == 429:
		return "RESOURCE_EXHAUSTED"
	case code == 501:
		return "UNIMPLEMENTED"
	case code == 502 || code == 503 || code == 529:
		return "UNAVAILABLE"
	case code == 504:
		return "DEADLINE_EXCEEDED"
	case code >= 400 && code < 500:
		return "FAILED_PRECONDITION"
	}
	return "INTERNAL"
}

func geminiError(status int, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}})
	return b
}

// writeGeminiJSON writes compact JSON.
func writeGeminiJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}
