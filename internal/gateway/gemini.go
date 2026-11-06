package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// ---- Gemini API ---------------------------------------------------------------
//
// Gemini is a client-facing protocol only: Gemini CLI and other Gemini API
// clients talk it to the gateway, and the upstream always speaks one of the
// three wire APIs. So this file parses Gemini requests into the IR, renders
// IR replies (whole or streamed) as Gemini responses, and nothing else.

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type gFunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gFunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type gPart struct {
	Text             *string            `json:"text,omitempty"`
	Thought          bool               `json:"thought,omitempty"`
	ThoughtSignature string             `json:"thoughtSignature,omitempty"`
	InlineData       *gBlob             `json:"inlineData,omitempty"`
	FileData         *gFileData         `json:"fileData,omitempty"`
	FunctionCall     *gFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *gFunctionResponse `json:"functionResponse,omitempty"`
}

type gContent struct {
	Role  string  `json:"role,omitempty"`
	Parts []gPart `json:"parts"`
}

type gFunctionDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type gTool struct {
	FunctionDeclarations []gFunctionDecl `json:"functionDeclarations,omitempty"`
}

type gFunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}

type gToolConfig struct {
	FunctionCallingConfig *gFunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

type gThinkingConfig struct {
	ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
	ThinkingLevel   string `json:"thinkingLevel,omitempty"`
	IncludeThoughts *bool  `json:"includeThoughts,omitempty"`
}

type gGenerationConfig struct {
	MaxOutputTokens    int              `json:"maxOutputTokens,omitempty"`
	Temperature        *float64         `json:"temperature,omitempty"`
	TopP               *float64         `json:"topP,omitempty"`
	StopSequences      []string         `json:"stopSequences,omitempty"`
	ResponseMimeType   string           `json:"responseMimeType,omitempty"`
	ResponseSchema     json.RawMessage  `json:"responseSchema,omitempty"`
	ResponseJSONSchema json.RawMessage  `json:"responseJsonSchema,omitempty"`
	ThinkingConfig     *gThinkingConfig `json:"thinkingConfig,omitempty"`
}

type gRequest struct {
	Contents          []gContent         `json:"contents"`
	SystemInstruction *gContent          `json:"systemInstruction,omitempty"`
	Tools             []gTool            `json:"tools,omitempty"`
	ToolConfig        *gToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *gGenerationConfig `json:"generationConfig,omitempty"`
}

// parseGemini reads a generateContent body into the IR. The body carries no
// model name (it is in the URL) and no stream flag (it is the method), so the
// caller sets Model and Stream.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	var msgs []Message
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		msgs = append(msgs, msg)
		i := len(msgs) - 1
		for _, p := range c.Parts {
			if part, ok := geminiPartIn(p, msgs); ok {
				msgs[i].Parts = append(msgs[i].Parts, part)
			}
		}
		if len(msgs[i].Parts) == 0 {
			msgs = msgs[:i]
		}
	}
	r.Messages = msgs

	var sys []string
	if si := g.SystemInstruction; si != nil {
		for _, p := range si.Parts {
			if p.Text != nil && *p.Text != "" {
				sys = append(sys, *p.Text)
			}
		}
	}

	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.ParametersJSONSchema
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			var s json.RawMessage
			if len(schema) > 0 {
				s = geminiSchema(schema)
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: s})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := *tc.FunctionCallingConfig
		r.ToolChoice = geminiToolChoice(fc)
		// Several names under ANY mean "one of these functions": only those
		// tools are offered, though the upstream still must call a tool.
		if strings.EqualFold(fc.Mode, "ANY") && len(fc.AllowedFunctionNames) > 1 {
			allowed := map[string]bool{}
			for _, n := range fc.AllowedFunctionNames {
				allowed[n] = true
			}
			kept := r.Tools[:0]
			for _, t := range r.Tools {
				if allowed[t.Name] {
					kept = append(kept, t)
				}
			}
			r.Tools = kept
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP, r.Stop = gc.Temperature, gc.TopP, gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			geminiThinking(r, *tc)
		}
		schema := gc.ResponseJSONSchema
		if len(schema) == 0 {
			schema = gc.ResponseSchema
		}
		// Upstreams differ in JSON mode and few of them offer it for every
		// model, so the instruction always goes into the system prompt.
		if strings.EqualFold(strings.TrimSpace(gc.ResponseMimeType), "application/json") || len(schema) > 0 {
			sys = append(sys, geminiJSONInstruction(schema))
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// geminiPartIn converts one Gemini part. msgs holds the turns read so far,
// which is where a function response looks for the call it answers.
func geminiPartIn(p gPart, msgs []Message) (Part, bool) {
	switch {
	case p.FunctionCall != nil:
		fc := p.FunctionCall
		id := fc.ID
		if id == "" {
			id = "call_" + newID()
		}
		return Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: parseArgs(string(fc.Args))}, true
	case p.FunctionResponse != nil:
		fr := p.FunctionResponse
		id := fr.ID
		if id == "" {
			id = geminiLastCallID(msgs, fr.Name)
		}
		if id == "" {
			id = "call_" + newID()
		}
		text, isErr := geminiResponseText(fr.Response)
		return Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr}, true
	case p.InlineData != nil:
		b := p.InlineData
		if strings.HasPrefix(b.MimeType, "image/") {
			return Part{Kind: Image, MediaType: b.MimeType, Data: b.Data}, true
		}
		return Part{Kind: Text, Text: fmt.Sprintf("[attachment omitted: %s file]", b.MimeType)}, true
	case p.FileData != nil:
		f := p.FileData
		if strings.HasPrefix(f.MimeType, "image/") && (strings.HasPrefix(f.FileURI, "http://") || strings.HasPrefix(f.FileURI, "https://")) {
			return Part{Kind: Image, MediaType: f.MimeType, URL: f.FileURI}, true
		}
		return Part{Kind: Text, Text: fmt.Sprintf("[file omitted: %s]", f.FileURI)}, true
	case p.Text != nil:
		if p.Thought {
			if *p.Text == "" && p.ThoughtSignature == "" {
				return Part{}, false
			}
			return Part{Kind: Thinking, Text: *p.Text, Signature: p.ThoughtSignature}, true
		}
		if *p.Text == "" {
			return Part{}, false
		}
		return Part{Kind: Text, Text: *p.Text}, true
	}
	return Part{}, false
}

// geminiLastCallID finds the most recent call of the named function. Gemini
// clients often leave call ids out, so results are matched by name.
func geminiLastCallID(msgs []Message, name string) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		for j := len(msgs[i].Parts) - 1; j >= 0; j-- {
			p := msgs[i].Parts[j]
			if p.Kind == ToolCall && p.Name == name {
				return p.ID
			}
		}
	}
	return ""
}

// geminiResponseText reads a function response. A response of the form
// {"output": "..."} or {"error": "..."} carries its text plainly; anything
// else is passed on as JSON.
func geminiResponseText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if v, ok := m["output"]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s, false
			}
		}
		if v, ok := m["error"]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s, true
			}
			return string(v), true
		}
	}
	return string(raw), false
}

// geminiToolChoice maps functionCallingConfig onto the IR's tool choice.
func geminiToolChoice(c gFunctionCallingConfig) string {
	switch strings.ToUpper(c.Mode) {
	case "NONE":
		return "none"
	case "ANY":
		if len(c.AllowedFunctionNames) == 1 {
			return "name:" + c.AllowedFunctionNames[0]
		}
		return "required"
	case "AUTO", "VALIDATED":
		return "auto"
	}
	return ""
}

// geminiThinking reads thinkingConfig. A budget of -1 (or any negative
// number) lets the model decide, which is treated as medium effort.
func geminiThinking(r *Request, tc gThinkingConfig) {
	switch {
	case tc.ThinkingBudget != nil:
		if b := *tc.ThinkingBudget; b < 0 {
			r.Effort = "medium"
		} else if b > 0 {
			r.Effort = effortOfBudget(b)
		}
	case tc.ThinkingLevel != "":
		r.Effort = effortOf(tc.ThinkingLevel)
	}
	// includeThoughts false hides reasoning from the reply; unset or true
	// keeps returning it.
	r.Thinking = r.Effort != "" || (tc.IncludeThoughts != nil && *tc.IncludeThoughts)
	r.HideThoughts = tc.IncludeThoughts != nil && !*tc.IncludeThoughts
}

// geminiJSONInstruction asks for bare JSON, with the schema when there is one.
func geminiJSONInstruction(schema json.RawMessage) string {
	s := "Respond only with a single JSON value. Do not add prose, explanations or code fences."
	if len(schema) > 0 {
		s += " The JSON must conform to this JSON schema:\n" + string(geminiSchema(schema))
	}
	return s
}

var geminiTypeNames = map[string]string{
	"OBJECT": "object", "STRING": "string", "NUMBER": "number", "INTEGER": "integer",
	"BOOLEAN": "boolean", "ARRAY": "array", "NULL": "null",
}

// geminiSchema converts a Gemini (OpenAPI-flavoured) schema to standard
// lowercase JSON Schema types. Unparseable input comes back unchanged.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, err := json.Marshal(geminiLowerTypes(v))
	if err != nil {
		return raw
	}
	return b
}

func geminiLowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "propertyOrdering")
		for k, val := range x {
			if k == "type" {
				switch t := val.(type) {
				case string:
					if l, ok := geminiTypeNames[t]; ok {
						x[k] = l
					}
					continue
				case []any:
					for i, e := range t {
						if s, ok := e.(string); ok {
							if l, ok := geminiTypeNames[s]; ok {
								t[i] = l
							}
						}
					}
					continue
				}
			}
			x[k] = geminiLowerTypes(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = geminiLowerTypes(x[i])
		}
		return x
	}
	return v
}

// ---- IR → Gemini replies ----------------------------------------------------

// geminiUsage is the usage metadata of a reply. The prompt count includes
// cache hits; reasoning tokens are counted apart from the candidate tokens.
func (u Usage) gemini() map[string]any {
	prompt := u.Input + u.CacheRead
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	m := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand,
		"totalTokenCount": prompt + cand + u.Reasoning}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	return m
}

// geminiFinish maps the IR stop reason onto a Gemini finishReason.
func geminiFinish(stop string) string {
	switch stop {
	case "length", "max_tokens":
		return "MAX_TOKENS"
	case "filter", "content_filter", "safety":
		return "SAFETY"
	}
	return "STOP"
}

// geminiCallPart is a function call as Gemini shows it; the arguments are
// always an object.
func geminiCallPart(p Part) map[string]any {
	id := p.ID
	if id == "" {
		id = "call_" + newID()
	}
	return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": parseArgs(argsString(p))}}
}

// geminiParts renders a reply's parts as Gemini parts.
func geminiParts(parts []Part) []map[string]any {
	out := []map[string]any{}
	for _, p := range parts {
		switch p.Kind {
		case Thinking:
			if p.Text == "" && p.Signature == "" {
				continue
			}
			m := map[string]any{"text": p.Text, "thought": true}
			if p.Signature != "" {
				m["thoughtSignature"] = p.Signature
			}
			out = append(out, m)
		case Text:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text})
			}
		case ToolCall:
			out = append(out, geminiCallPart(p))
		}
	}
	return out
}

// renderGemini is the non-streaming reply, a single GenerateContentResponse.
func renderGemini(res Result, model string) []byte {
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "gem-" + newID()
	}
	cand := map[string]any{
		"content":      map[string]any{"role": "model", "parts": geminiParts(res.Parts)},
		"finishReason": geminiFinish(res.Stop),
		"index":        0,
	}
	b, _ := json.Marshal(map[string]any{"candidates": []map[string]any{cand},
		"usageMetadata": res.Usage.gemini(), "modelVersion": model, "responseId": id})
	return b
}

// geminiEncoder writes IR events as a Gemini SSE stream. Each event is one
// GenerateContentResponse. A function call is held back until it is complete,
// so a client never receives a half-formed call.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector
	call  *geminiPendingCall
	// failed is set once an error event went out; the stream then ends
	// without a STOP chunk.
	failed bool
}

type geminiPendingCall struct {
	id, name string
	args     strings.Builder
}

var _ streamEncoder = (*geminiEncoder)(nil)

func (e *geminiEncoder) responseID() string {
	if e.id == "" {
		e.id = "gem-" + newID()
	}
	return e.id
}

func (e *geminiEncoder) chunk(parts []map[string]any, finish any, usage any) {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != nil {
		cand["finishReason"] = finish
	}
	msg := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": e.model, "responseId": e.responseID()}
	if usage != nil {
		msg["usageMetadata"] = usage
	}
	e.w.event("", msg)
}

// flushCall sends the open function call, complete, if there is one.
func (e *geminiEncoder) flushCall() {
	c := e.call
	if c == nil {
		return
	}
	e.call = nil
	e.chunk([]map[string]any{{"functionCall": map[string]any{"id": c.id, "name": c.name,
		"args": parseArgs(c.args.String())}}}, nil, nil)
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if e.id == "" {
			e.id = ev.MsgID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
	case KText:
		e.flushCall()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text}}, nil, nil)
		}
	case KThink:
		e.flushCall()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text, "thought": true}}, nil, nil)
		}
	case KToolStart:
		e.flushCall()
		id := ev.ID
		if id == "" {
			id = "call_" + newID()
		}
		e.call = &geminiPendingCall{id: id, name: ev.Name}
	case KToolArgs:
		if e.call != nil {
			e.call.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushCall()
	case KError:
		e.flushCall()
		e.failed = true
		e.w.event("", geminiErrorBody(http.StatusInternalServerError, ev.Text))
	}
	e.col.add(ev)
}

// finish ends the stream with the finish reason and usage.
func (e *geminiEncoder) finish() {
	e.flushCall()
	if e.failed {
		return
	}
	res := e.col.finish()
	model := e.model
	if res.Model != "" {
		model = res.Model
	}
	e.model = model
	e.chunk([]map[string]any{}, geminiFinish(res.Stop), res.Usage.gemini())
}

// ---- errors -------------------------------------------------------------------

// geminiStatus is the gRPC-style status name Gemini uses for an HTTP code.
func geminiStatus(code int) string {
	switch code {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	case http.StatusBadGateway, http.StatusServiceUnavailable, 529:
		return "UNAVAILABLE"
	}
	return "INTERNAL"
}

// geminiErrorBody is an error in Gemini's shape.
func geminiErrorBody(code int, msg string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]any{"code": code, "message": msg, "status": geminiStatus(code)}})
	return b
}

// geminiWriteError answers in Gemini's error shape and returns the status.
func geminiWriteError(w http.ResponseWriter, code int, msg string) int {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(geminiErrorBody(code, msg))
	return code
}
