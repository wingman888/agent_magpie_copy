package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// The Gemini API is served as a client protocol only: every provider is
// reached over Chat, Responses or Anthropic, and the translation happens
// here. geminiProto labels calls that came in over it; it is deliberately
// not one of provider.Protocols.
const geminiProto provider.Protocol = "gemini"

// geminiMethods are the model methods served for every model.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// ---- wire types ----------------------------------------------------------------

type gInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFileData struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gFuncCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gFuncResp struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gPart struct {
	Text             string     `json:"text"`
	Thought          bool       `json:"thought"`
	ThoughtSignature string     `json:"thoughtSignature"`
	InlineData       *gInline   `json:"inlineData"`
	FileData         *gFileData `json:"fileData"`
	FunctionCall     *gFuncCall `json:"functionCall"`
	FunctionResponse *gFuncResp `json:"functionResponse"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
}

type gGenConfig struct {
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
}

type gRequest struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Tools             []struct {
		FunctionDeclarations []gDecl `json:"functionDeclarations"`
	} `json:"tools"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *gGenConfig `json:"generationConfig"`
}

// ---- request side ----------------------------------------------------------------

// parseGemini turns a generateContent body into the shared request shape.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	// function calls without ids are paired with responses by name: a
	// response goes to the most recent call of the same function.
	lastByName := map[string]string{}
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + newID()
				}
				lastByName[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiArgs(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = lastByName[fr.Name]
				}
				if id == "" {
					id = "call_" + newID()
				}
				text, isErr := geminiResponseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			case p.InlineData != nil:
				d := p.InlineData
				if strings.HasPrefix(d.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: d.MimeType, Data: d.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text,
						Text: fmt.Sprintf("[attachment omitted: %s, about %d bytes]", d.MimeType, len(d.Data)*3/4)})
				}
			case p.FileData != nil:
				f := p.FileData
				if strings.HasPrefix(f.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: f.MimeType, URL: f.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file: %s (%s)]", f.FileURI, f.MimeType)})
				}
			case p.Thought:
				if p.Text != "" || p.ThoughtSignature != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
				}
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			}
		}
		if len(msg.Parts) == 0 {
			continue
		}
		for _, part := range msg.Parts {
			if part.Kind == ToolResult {
				msg.Role = "user" // results are always what the user side returns
				break
			}
		}
		r.Messages = append(r.Messages, msg)
	}
	if si := g.SystemInstruction; si != nil {
		var sys []string
		for _, p := range si.Parts {
			if p.Text != "" && !p.Thought {
				sys = append(sys, p.Text)
			}
		}
		r.System = strings.Join(sys, "\n\n")
	}
	for _, t := range g.Tools {
		for _, d := range t.FunctionDeclarations {
			if d.Name == "" {
				continue
			}
			schema := d.ParametersJSONSchema
			if len(schema) == 0 {
				schema = d.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: d.Name, Description: d.Description, Schema: lowerSchema(schema)})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			names := fc.AllowedFunctionNames
			if len(names) == 0 {
				for _, t := range r.Tools {
					names = append(names, t.Name)
				}
			}
			if len(names) == 1 {
				r.ToolChoice = "name:" + names[0]
			} else {
				// several allowed: offer only those tools, then require one
				var keep []Tool
				for _, t := range r.Tools {
					for _, n := range names {
						if t.Name == n {
							keep = append(keep, t)
							break
						}
					}
				}
				r.Tools = keep
				r.ToolChoice = "required"
			}
		default:
			r.ToolChoice = "auto"
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp = gc.Temperature
		r.TopP = gc.TopP
		r.Stop = gc.StopSequences
		schema := gc.ResponseJSONSchema
		if len(schema) == 0 {
			schema = gc.ResponseSchema
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(gc.ResponseMimeType)), "application/json") || len(schema) > 0 {
			r.System = joinSystem(r.System, geminiJSONInstruction(lowerSchema(schema)))
		}
		if tc := gc.ThinkingConfig; tc != nil {
			if e := effortOf(tc.ThinkingLevel); e != "" {
				r.Effort = e
			} else if tc.ThinkingBudget != nil {
				switch b := *tc.ThinkingBudget; {
				case b == -1: // the model decides; treat as medium
					r.Effort = "medium"
				case b > 0:
					r.Effort = effortOfBudget(b)
				}
			}
			r.Thinking = r.Effort != "" || tc.IncludeThoughts
		}
	}
	return r, nil
}

// geminiArgs is a function call's arguments as a JSON object.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return parseArgs(s)
}

// geminiResponseText reads a function response: a single "output" string is
// used as is, anything else is passed on as its JSON text.
func geminiResponseText(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) == nil {
		if out, ok := m["output"].(string); ok && len(m) == 1 {
			return out, false
		}
		_, isErr := m["error"]
		return s, isErr
	}
	return s, false
}

// geminiJSONInstruction asks for JSON in place of a response format field,
// which the upstream protocols lack.
func geminiJSONInstruction(schema json.RawMessage) string {
	if len(schema) == 0 {
		return "Respond only with a valid JSON value. Output no prose and no code fences."
	}
	return "Respond only with a JSON value that conforms to this JSON schema, with no prose and no code fences:\n" + string(schema)
}

func joinSystem(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

// lowerSchema converts a Gemini (OpenAPI style, upper-case) schema to JSON
// Schema, lower-casing every "type".
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	if v == nil {
		return nil
	}
	lowerTypes(v)
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

func lowerTypes(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "type" {
				switch t := val.(type) {
				case string:
					x[k] = strings.ToLower(t)
					continue
				case []any:
					for i, e := range t {
						if s, ok := e.(string); ok {
							t[i] = strings.ToLower(s)
						}
					}
					continue
				}
			}
			lowerTypes(val)
		}
	case []any:
		for _, e := range x {
			lowerTypes(e)
		}
	}
}

// geminiEstimate is the rough token count for countTokens: a quarter of the
// characters the request carries.
func geminiEstimate(body []byte) (int, error) {
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return 0, fmt.Errorf("invalid request: %v", err)
	}
	src := body
	if len(wrap.GenerateContentRequest) > 0 {
		src = wrap.GenerateContentRequest
	}
	req, err := parseGemini(src)
	if err != nil {
		return 0, err
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return n / 4, nil
}

// ---- response side ---------------------------------------------------------------

func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage is the usageMetadata block. The prompt count includes cache
// reads, as the Chat API's does; candidates carry the upstream's output count.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	m := map[string]any{
		"promptTokenCount":     prompt,
		"candidatesTokenCount": u.Output,
		"totalTokenCount":      prompt + u.Output,
		"thoughtsTokenCount":   u.Reasoning,
	}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

// renderGemini is the non-streaming reply, compact JSON.
func renderGemini(res Result, model string) []byte {
	parts := []any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case Thinking:
			m := map[string]any{"text": p.Text, "thought": true}
			if p.Signature != "" {
				m["thoughtSignature"] = p.Signature
			}
			parts = append(parts, m)
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}})
		}
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
	return b
}

// geminiEvent is one streamed chunk holding a single part.
func geminiEvent(parts []any) map[string]any {
	return map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts}}}}
}

type gemTool struct {
	id, name string
}

// geminiEncoder writes a reply as Gemini stream chunks: one part per event,
// in the order the reply produced them. A function call is sent once its
// arguments are complete. The last part is held back so it can carry the
// finishReason and usage.
type geminiEncoder struct {
	send   func(any)
	model  string
	id     string
	pend   map[string]any // part waiting to be sent with the final event
	tool   *gemTool       // function call being streamed
	args   strings.Builder
	stop   string
	failed bool
	usage  Usage
}

func (e *geminiEncoder) flushPart() {
	if e.pend != nil {
		e.send(geminiEvent([]any{e.pend}))
		e.pend = nil
	}
}

func (e *geminiEncoder) push(part map[string]any) {
	e.flushPart()
	e.pend = part
}

func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	t := e.tool
	e.tool = nil
	args := parseArgs(e.args.String())
	e.args.Reset()
	e.push(map[string]any{"functionCall": map[string]any{"id": t.id, "name": t.name, "args": args}})
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
		e.usage.add(ev.Usage)
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.push(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.push(map[string]any{"text": ev.Text, "thought": true})
		}
	case KSig:
		if e.pend != nil {
			if th, ok := e.pend["thought"].(bool); ok && th {
				e.pend["thoughtSignature"] = ev.Text
			}
		}
	case KToolStart:
		e.flushTool()
		id := ev.ID
		if id == "" {
			id = "call_" + newID()
		}
		e.tool = &gemTool{id: id, name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushTool()
		e.stop = ev.Stop
	case KUsage:
		e.usage.add(ev.Usage)
	case KError:
		e.flushTool()
		e.flushPart()
		e.failed = true
		e.send(map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	if e.failed {
		return
	}
	if e.id == "" {
		e.id = newID()
	}
	cand := map[string]any{
		"content":      map[string]any{"role": "model", "parts": []any{}},
		"finishReason": geminiFinish(e.stop),
	}
	if e.pend != nil {
		cand["content"] = map[string]any{"role": "model", "parts": []any{e.pend}}
		e.pend = nil
	}
	e.send(map[string]any{
		"candidates":    []any{cand},
		"usageMetadata": geminiUsage(e.usage),
		"modelVersion":  e.model,
		"responseId":    e.id,
	})
}

// ---- HTTP ------------------------------------------------------------------------

var geminiStatus = map[int]string{
	400: "INVALID_ARGUMENT",
	401: "UNAUTHENTICATED",
	403: "PERMISSION_DENIED",
	404: "NOT_FOUND",
	429: "RESOURCE_EXHAUSTED",
	500: "INTERNAL",
	502: "UNAVAILABLE",
	503: "UNAVAILABLE",
	504: "DEADLINE_EXCEEDED",
}

func writeGeminiJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// writeGeminiError answers in the Gemini error shape.
func writeGeminiError(w http.ResponseWriter, status int, msg string) int {
	st, ok := geminiStatus[status]
	if !ok {
		st = "UNKNOWN"
		if status >= 500 {
			st = "INTERNAL"
		}
	}
	writeGeminiJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "status": st}})
	return status
}

// geminiModels lists the catalog in the Gemini shape.
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
			"description":                e.Provider.Name,
			"supportedGenerationMethods": geminiMethods,
		})
	}
	writeGeminiJSON(w, http.StatusOK, map[string]any{"models": models})
}

// gemini serves POST /v1beta/models/{model}:{method}. The model reference may
// contain slashes and colons; the method is what follows the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i <= 0 {
		writeGeminiError(w, 404, fmt.Sprintf("%q is not a Gemini method path; use /v1beta/models/{model}:generateContent", rest))
		return
	}
	model, method := rest[:i], rest[i+1:]
	if !slices.Contains(geminiMethods, method) {
		writeGeminiError(w, 404, fmt.Sprintf("unknown method %q; dial serves generateContent, streamGenerateContent and countTokens", method))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, 400, err.Error())
		return
	}
	start := time.Now()
	call := Call{Time: start, From: geminiProto, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, pm, ok := provider.Resolve(model)
	if !ok {
		msg := fmt.Sprintf("dial knows no model %q", model)
		if ids := provider.IDs(); len(ids) > 0 {
			msg += "; it has " + strings.Join(ids, ", ")
		} else {
			msg += "; add a provider in dial first"
		}
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeGeminiError(w, 404, msg)
		return
	}
	if method == "countTokens" {
		n, err := geminiEstimate(body)
		if err != nil {
			writeGeminiError(w, 400, err.Error())
			return
		}
		writeGeminiJSON(w, http.StatusOK, map[string]any{"totalTokens": n})
		return
	}
	call.Provider = p.ID
	to := p.Speaks()
	if len(to) == 0 {
		writeGeminiError(w, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	stream := method == "streamGenerateContent"
	sse := r.URL.Query().Get("alt") == "sse"
	call.Status, call.Error = s.geminiTranslate(w, r, p, to[0], model, pm, body, stream, sse, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: pm,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiTranslate serves a Gemini request by speaking the provider's protocol
// to it. ref is the model as the client named it; model is the vendor's.
// The provider is always streamed, the client gets the reply shape it asked
// for.
func (s *Server) geminiTranslate(w http.ResponseWriter, r *http.Request, p provider.Provider, to provider.Protocol, ref, model string, body []byte, stream, sse bool, u *Usage) (int, string) {
	req, err := parseGemini(body)
	if err != nil {
		return writeGeminiError(w, 400, err.Error()), err.Error()
	}
	req.Model = model
	req.Stream = true
	res, err := s.forward(r.Context(), p, to, pathOf(to), build(to, req, model, p.Host()), r.Header)
	if err != nil {
		msg := p.Name + ": " + err.Error()
		return writeGeminiError(w, 502, msg), msg
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeGeminiError(w, res.StatusCode, msg), msg
	}
	rd, isStream := eventStreamBody(res)
	if !isStream {
		b, _ := io.ReadAll(io.LimitReader(rd, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeGeminiError(w, 502, msg), msg
	}
	dec := decoder(to)
	if !stream {
		var col collector
		readSSE(rd, func(_, data string) error {
			return dec(data, col.add)
		})
		if col.err != "" && len(col.res.Parts) == 0 {
			return writeGeminiError(w, 502, p.Name+": "+col.err), col.err
		}
		res2 := col.finish()
		u.add(res2.Usage)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(renderGemini(res2, ref))
		return http.StatusOK, col.err
	}

	// streaming: SSE when alt=sse, otherwise a JSON array of the chunks
	var chunks []any
	var sw *sseWriter
	if sse {
		sw = newSSEWriter(w)
		sw.begin()
	}
	enc := &geminiEncoder{model: ref, send: func(v any) {
		if sw != nil {
			sw.event("", v)
		} else {
			chunks = append(chunks, v)
		}
	}}
	var failed string
	readSSE(rd, func(_, data string) error {
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
	if sw == nil {
		if chunks == nil {
			chunks = []any{}
		}
		writeGeminiJSON(w, http.StatusOK, chunks)
	}
	return http.StatusOK, failed
}
