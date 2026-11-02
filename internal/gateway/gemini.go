package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/yetone/dial/internal/provider"
)

// ---- Google Gemini API --------------------------------------------------------
//
// dial serves the Gemini API to clients (Gemini CLI and friends) but never
// speaks it upstream: every Gemini call is translated to one of the three
// vendor protocols.

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFile struct {
	MimeType string `json:"mimeType"`
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
	FileData         *gFile             `json:"fileData,omitempty"`
	FunctionCall     *gFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *gFunctionResponse `json:"functionResponse,omitempty"`
}

type gContent struct {
	Role  string  `json:"role,omitempty"`
	Parts []gPart `json:"parts"`
}

type gRequest struct {
	Contents          []gContent      `json:"contents"`
	SystemInstruction json.RawMessage `json:"systemInstruction,omitempty"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name                 string          `json:"name"`
			Description          string          `json:"description,omitempty"`
			Parameters           json.RawMessage `json:"parameters,omitempty"`
			ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
		} `json:"functionDeclarations,omitempty"`
	} `json:"tools,omitempty"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode,omitempty"`
			AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
		} `json:"functionCallingConfig,omitempty"`
	} `json:"toolConfig,omitempty"`
	GenerationConfig *struct {
		MaxOutputTokens    int             `json:"maxOutputTokens,omitempty"`
		Temperature        *float64        `json:"temperature,omitempty"`
		TopP               *float64        `json:"topP,omitempty"`
		StopSequences      []string        `json:"stopSequences,omitempty"`
		ResponseMimeType   string          `json:"responseMimeType,omitempty"`
		ResponseSchema     json.RawMessage `json:"responseSchema,omitempty"`
		ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
		ThinkingConfig     *struct {
			ThinkingBudget  *int   `json:"thinkingBudget,omitempty"`
			ThinkingLevel   string `json:"thinkingLevel,omitempty"`
			IncludeThoughts bool   `json:"includeThoughts,omitempty"`
		} `json:"thinkingConfig,omitempty"`
	} `json:"generationConfig,omitempty"`
}

var callSeq atomic.Int64

// newCallID makes a tool call id for calls the client sent without one.
func newCallID() string {
	return fmt.Sprintf("call_%s_%d", newID(), callSeq.Add(1))
}

// parseGemini reads a generateContent request body. The model and whether
// to stream come from the URL, so the caller fills those in.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{System: geminiSystem(g.SystemInstruction)}

	// calls seen so far, to give id-less function responses the id of the
	// latest unanswered call of the same name
	type seen struct {
		id, name string
		answered bool
	}
	var calls []*seen
	answer := func(id, name string) string {
		if id != "" {
			for i := len(calls) - 1; i >= 0; i-- {
				if calls[i].id == id {
					calls[i].answered = true
					break
				}
			}
			return id
		}
		for i := len(calls) - 1; i >= 0; i-- {
			if calls[i].name == name && !calls[i].answered {
				calls[i].answered = true
				return calls[i].id
			}
		}
		for i := len(calls) - 1; i >= 0; i-- {
			if calls[i].name == name {
				return calls[i].id
			}
		}
		return newCallID()
	}

	for _, c := range g.Contents {
		role := "user"
		if c.Role == "model" || c.Role == "assistant" {
			role = "assistant"
		}
		msg := Message{Role: role}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					id = newCallID()
				}
				calls = append(calls, &seen{id: id, name: p.FunctionCall.Name})
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name,
					Args: geminiArgs(p.FunctionCall.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				out, isErr := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: answer(fr.ID, fr.Name), Text: out, IsError: isErr})
			case p.InlineData != nil:
				msg.Parts = append(msg.Parts, geminiMedia(p.InlineData.MimeType, p.InlineData.Data, ""))
			case p.FileData != nil:
				msg.Parts = append(msg.Parts, geminiMedia(p.FileData.MimeType, "", p.FileData.FileURI))
			case p.Text != nil:
				if p.Thought {
					if *p.Text != "" || p.ThoughtSignature != "" {
						msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: *p.Text, Signature: p.ThoughtSignature})
					}
				} else if *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
				}
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}

	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.ParametersJSONSchema
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: geminiSchema(schema)})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			r.ToolChoice = "required"
			if len(fc.AllowedFunctionNames) > 0 {
				allowed := map[string]bool{}
				for _, n := range fc.AllowedFunctionNames {
					allowed[n] = true
				}
				var keep []Tool
				for _, t := range r.Tools {
					if allowed[t.Name] {
						keep = append(keep, t)
					}
				}
				if len(keep) > 0 {
					r.Tools = keep
				}
				if len(fc.AllowedFunctionNames) == 1 {
					r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
				}
			}
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		schema := gc.ResponseJSONSchema
		if len(schema) == 0 {
			schema = gc.ResponseSchema
		}
		if len(schema) > 0 || strings.EqualFold(strings.TrimSpace(gc.ResponseMimeType), "application/json") {
			r.JSON = true
			r.JSONSchema = geminiSchema(schema)
		}
		if tc := gc.ThinkingConfig; tc != nil {
			off := false
			if b := tc.ThinkingBudget; b != nil {
				switch {
				case *b < 0: // dynamic: the model decides
					r.Effort = "medium"
				case *b == 0:
					off = true
				default:
					r.Effort = effortOfBudget(*b)
				}
			}
			if e := effortOf(tc.ThinkingLevel); e != "" {
				r.Effort, off = e, false
			}
			if tc.IncludeThoughts && !off {
				r.Thinking = true
			}
		}
	}
	return r, nil
}

// geminiSystem reads systemInstruction: a Content, or a bare string.
func geminiSystem(raw json.RawMessage) string {
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
	var out []string
	for _, p := range c.Parts {
		if p.Text != nil && *p.Text != "" && !p.Thought {
			out = append(out, *p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// geminiMedia turns inline data or a file reference into an image, or a
// placeholder for anything else: other APIs take no arbitrary attachments.
func geminiMedia(mime, data, uri string) Part {
	if strings.HasPrefix(strings.ToLower(mime), "image/") {
		if data != "" {
			return Part{Kind: Image, MediaType: mime, Data: data}
		}
		if uri != "" {
			return Part{Kind: Image, URL: uri}
		}
	}
	if mime == "" {
		mime = "unknown type"
	}
	desc := "[attachment of type " + mime + " omitted: this model only accepts text and images]"
	if uri != "" {
		desc = "[attachment " + uri + " of type " + mime + " omitted: this model only accepts text and images]"
	}
	return Part{Kind: Text, Text: desc}
}

// geminiArgs is a function call's arguments as a JSON object.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return parseArgs(s)
}

// geminiResult reads a functionResponse's response object into the text a
// tool result carries. {"output": "..."} and {"error": "..."} — what Gemini
// CLI sends — become their string; anything else stays JSON.
func geminiResult(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && len(m) == 1 {
		for k, v := range m {
			var str string
			if json.Unmarshal(v, &str) == nil {
				switch k {
				case "output", "content", "result":
					return str, false
				case "error":
					return str, true
				}
			}
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil {
		return buf.String(), false
	}
	return s, false
}

// geminiTypes are the upper-case type names of Gemini's schema dialect.
var geminiTypes = map[string]string{"STRING": "string", "NUMBER": "number", "INTEGER": "integer",
	"BOOLEAN": "boolean", "ARRAY": "array", "OBJECT": "object", "NULL": "null"}

// geminiSchema rewrites a Gemini schema as standard JSON Schema: type names
// lower-cased, nullable folded into the type, Gemini-only keys dropped.
// Standard JSON Schema passes through unchanged.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(fixSchema(v))
	if err != nil {
		return raw
	}
	return out
}

func fixSchema(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	lower := func(t any) any {
		s, ok := t.(string)
		if !ok {
			return t
		}
		if l, ok := geminiTypes[strings.ToUpper(s)]; ok {
			return l
		}
		if strings.EqualFold(s, "TYPE_UNSPECIFIED") {
			return nil
		}
		return s
	}
	switch t := m["type"].(type) {
	case string:
		if l := lower(t); l == nil {
			delete(m, "type")
		} else {
			m["type"] = l
		}
	case []any:
		for i := range t {
			t[i] = lower(t[i])
		}
	}
	if n, ok := m["nullable"].(bool); ok {
		delete(m, "nullable")
		if t, ok := m["type"].(string); ok && n && t != "null" {
			m["type"] = []any{t, "null"}
		}
	}
	delete(m, "propertyOrdering")
	for _, k := range []string{"items", "additionalProperties", "not", "contains", "if", "then", "else"} {
		if s, ok := m[k]; ok {
			m[k] = fixSchema(s)
		}
	}
	for _, k := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if list, ok := m[k].([]any); ok {
			for i := range list {
				list[i] = fixSchema(list[i])
			}
		}
	}
	for _, k := range []string{"properties", "$defs", "definitions", "patternProperties"} {
		if props, ok := m[k].(map[string]any); ok {
			for name, s := range props {
				props[name] = fixSchema(s)
			}
		}
	}
	return m
}

// ---- replies ------------------------------------------------------------------

func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// gemini is the usage block. promptTokenCount includes cached tokens;
// thinking is counted apart from candidatesTokenCount.
func (u Usage) gemini() map[string]any {
	prompt := u.Input + u.CacheRead + u.CacheWrite
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	m := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand, "totalTokenCount": prompt + cand + u.Reasoning}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	return m
}

func geminiCallPart(p Part) map[string]any {
	id := p.ID
	if id == "" {
		id = newCallID()
	}
	return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": geminiArgs(argsOf(p))}}
}

func geminiThoughtPart(text, sig string) map[string]any {
	part := map[string]any{"text": text, "thought": true}
	if sig != "" {
		part["thoughtSignature"] = sig
	}
	return part
}

func geminiChunk(parts []map[string]any, finish string, usage map[string]any, model, id string) map[string]any {
	if parts == nil {
		parts = []map[string]any{}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	out := map[string]any{"candidates": []any{cand}, "modelVersion": model, "responseId": id}
	if usage != nil {
		out["usageMetadata"] = usage
	}
	return out
}

// geminiEncoder writes events as a Gemini SSE stream, each event one
// GenerateContentResponse. One chunk is always held back so the last one
// can carry finishReason and usage alongside real content, and function
// calls go out only once their arguments are complete.
type geminiEncoder struct {
	w       *sseWriter
	model   string
	id      string
	held    []map[string]any
	calls   bool // held is made only of function calls
	tool    *Part
	args    strings.Builder
	col     collector
	started bool
}

func (e *geminiEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started = true
	e.id = ev.MsgID
	if e.id == "" {
		e.id = newID()
	}
}

func (e *geminiEncoder) flush() {
	if e.held != nil {
		e.w.event("", geminiChunk(e.held, "", nil, e.model, e.id))
		e.held, e.calls = nil, false
	}
}

func (e *geminiEncoder) send(part map[string]any, call bool) {
	if call && e.calls {
		e.held = append(e.held, part)
		return
	}
	e.flush()
	e.held, e.calls = []map[string]any{part}, call
}

func (e *geminiEncoder) closeTool() {
	if e.tool == nil {
		return
	}
	p := *e.tool
	p.Args = parseArgs(e.args.String())
	e.tool = nil
	e.args.Reset()
	e.send(geminiCallPart(p), true)
}

func (e *geminiEncoder) event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		if ev.Text != "" {
			e.closeTool()
			e.send(map[string]any{"text": ev.Text}, false)
		}
	case KThink:
		if ev.Text != "" {
			e.closeTool()
			e.send(geminiThoughtPart(ev.Text, ""), false)
		}
	case KSig:
		if n := len(e.held); n > 0 && e.held[n-1]["thought"] == true {
			sig, _ := e.held[n-1]["thoughtSignature"].(string)
			e.held[n-1]["thoughtSignature"] = sig + ev.Text
		}
	case KToolStart:
		e.closeTool()
		e.tool = &Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.closeTool()
	case KError:
		e.closeTool()
		e.flush()
		e.w.event("", map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.closeTool()
	res := e.col.finish()
	parts := e.held
	e.held = nil
	e.w.event("", geminiChunk(parts, geminiFinish(res.Stop), res.Usage.gemini(), e.model, e.id))
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	var parts []map[string]any
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text != "" || p.Signature != "" {
				parts = append(parts, geminiThoughtPart(p.Text, p.Signature))
			}
		case ToolCall:
			parts = append(parts, geminiCallPart(p))
		}
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(geminiChunk(parts, geminiFinish(res.Stop), res.Usage.gemini(), model, id))
	return b
}

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
	case 409:
		return "ABORTED"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 499:
		return "CANCELLED"
	case 501:
		return "UNIMPLEMENTED"
	case 502, 503, 529:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	if code >= 400 && code < 500 {
		return "FAILED_PRECONDITION"
	}
	return "INTERNAL"
}

// ---- HTTP -----------------------------------------------------------------------

var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

func geminiModelObject(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "baseModelId": e.ID, "version": "dial", "displayName": name,
		"description":                name + " from " + e.Provider.Name + ", served by dial",
		"supportedGenerationMethods": geminiMethods}
}

// geminiModels lists the whole catalog the Gemini way.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("rest"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, fmt.Sprintf("dial knows no model %q", id))
}

// splitGeminiPath splits "fake/m1:generateContent" at the last colon; model
// ids may contain colons themselves.
func splitGeminiPath(rest string) (model, method string, ok bool) {
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimPrefix(rest[:i], "models/"), rest[i+1:], true
}

// gemini serves POST /v1beta/models/{model}:{method}.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	model, method, ok := splitGeminiPath(r.PathValue("rest"))
	if !ok {
		writeError(w, provider.Gemini, 404, "dial serves "+apiList)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	switch method {
	case "generateContent", "streamGenerateContent":
		stream := method == "streamGenerateContent"
		s.serve(w, r, provider.Gemini, model, body, func() (*Request, error) {
			req, err := parseGemini(body)
			if err != nil {
				return nil, err
			}
			req.Model, req.Stream = model, stream
			req.plainStream = stream && r.URL.Query().Get("alt") != "sse"
			return req, nil
		})
	case "countTokens":
		s.geminiCountTokens(w, model, body)
	default:
		writeError(w, provider.Gemini, 404, fmt.Sprintf("unknown method %q; dial serves %s", method, strings.Join(geminiMethods, ", ")))
	}
}

// geminiCountTokens is a rough estimate: about four characters a token.
func (s *Server) geminiCountTokens(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, provider.Gemini, 404, fmt.Sprintf("dial knows no model %q", model))
		return
	}
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 && string(wrap.GenerateContentRequest) != "null" {
		body = wrap.GenerateContentRequest
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	n, images := len(req.System), 0
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
			if p.Kind == Image {
				images++
			}
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	total := (n+3)/4 + images*258
	writeJSON(w, 200, map[string]any{"totalTokens": total})
}
