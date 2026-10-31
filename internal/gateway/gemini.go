package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// ---- Gemini API (client side only) --------------------------------------------
//
// Gemini CLI and other Gemini clients call
//   POST /v1beta/models/{model}:generateContent
//   POST /v1beta/models/{model}:streamGenerateContent?alt=sse
// where {model} is a catalog id. The model and the stream flag live in the
// URL, so the handler copies them into the body as "model" and "stream"
// and the rest of the path is the same as for every other API.

type gInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gPart struct {
	Text             *string  `json:"text,omitempty"`
	Thought          bool     `json:"thought,omitempty"`
	ThoughtSignature string   `json:"thoughtSignature,omitempty"`
	InlineData       *gInline `json:"inlineData,omitempty"`
	FileData         *struct {
		MimeType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData,omitempty"`
	FunctionCall *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall,omitempty"`
	FunctionResponse *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse,omitempty"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gRequest struct {
	Model             string      `json:"model"`
	Stream            bool        `json:"stream"`
	Contents          []gContent  `json:"contents"`
	SystemInstruction *gContent   `json:"systemInstruction"`
	SystemInstrSnake  *gContent   `json:"system_instruction"`
	Tools             []gToolSpec `json:"tools"`
	ToolConfig        *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *struct {
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

type gToolSpec struct {
	FunctionDeclarations []struct {
		Name                 string          `json:"name"`
		Description          string          `json:"description"`
		Parameters           json.RawMessage `json:"parameters"`
		ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
	} `json:"functionDeclarations"`
}

func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}

	// system instruction
	si := g.SystemInstruction
	if si == nil {
		si = g.SystemInstrSnake
	}
	if si != nil {
		var ss []string
		for _, p := range si.Parts {
			if p.Text != nil && *p.Text != "" {
				ss = append(ss, *p.Text)
			}
		}
		r.System = strings.Join(ss, "\n")
	}

	// conversation. Calls the client sent without an id get one here, and a
	// result without an id is matched to the latest call of that name.
	type callRef struct {
		id, name string
		used     bool
	}
	var calls []callRef
	nextID := 0
	genID := func() string { nextID++; return fmt.Sprintf("call_%d", nextID) }
	for _, c := range g.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		msg := Message{Role: role}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					id = genID()
				}
				calls = append(calls, callRef{id: id, name: p.FunctionCall.Name})
				args := p.FunctionCall.Args
				if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
					args = json.RawMessage("{}")
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: args})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id != "" {
					for i := range calls {
						if calls[i].id == id {
							calls[i].used = true
						}
					}
				} else {
					pick := -1
					for i := len(calls) - 1; i >= 0 && pick < 0; i-- {
						if calls[i].name == fr.Name && !calls[i].used {
							pick = i
						}
					}
					for i := len(calls) - 1; i >= 0 && pick < 0; i-- {
						if calls[i].name == fr.Name {
							pick = i
						}
					}
					if pick >= 0 {
						calls[pick].used = true
						id = calls[pick].id
					} else {
						id = genID()
						calls = append(calls, callRef{id: id, name: fr.Name, used: true})
					}
				}
				out, isErr := gResponseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: isErr})
			case p.InlineData != nil:
				if strings.HasPrefix(strings.ToLower(p.InlineData.MimeType), "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment: " + p.InlineData.MimeType + "]"})
				}
			case p.FileData != nil:
				if strings.HasPrefix(strings.ToLower(p.FileData.MimeType), "image/") && strings.HasPrefix(p.FileData.FileURI, "http") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment: " + p.FileData.MimeType + "]"})
				}
			case p.Thought:
				text := ""
				if p.Text != nil {
					text = *p.Text
				}
				if n := len(msg.Parts); n > 0 && msg.Parts[n-1].Kind == Thinking {
					msg.Parts[n-1].Text += text
					if p.ThoughtSignature != "" {
						msg.Parts[n-1].Signature = p.ThoughtSignature
					}
				} else if text != "" || p.ThoughtSignature != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: text, Signature: p.ThoughtSignature})
				}
			case p.Text != nil:
				if *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
				}
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}

	// tools and how freely to use them
	var allowed []string
	mode := ""
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		mode = strings.ToUpper(g.ToolConfig.FunctionCallingConfig.Mode)
		allowed = g.ToolConfig.FunctionCallingConfig.AllowedFunctionNames
	}
	for _, ts := range g.Tools {
		for _, d := range ts.FunctionDeclarations {
			schema := d.ParametersJSONSchema
			if len(bytes.TrimSpace(schema)) == 0 || string(bytes.TrimSpace(schema)) == "null" {
				schema = d.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: d.Name, Description: d.Description, Schema: geminiSchema(schema, true)})
		}
	}
	if len(allowed) > 0 && len(r.Tools) > 0 {
		var keep []Tool
		for _, t := range r.Tools {
			for _, a := range allowed {
				if a == t.Name {
					keep = append(keep, t)
					break
				}
			}
		}
		if len(keep) > 0 {
			r.Tools = keep
		}
	}
	switch mode {
	case "NONE":
		r.ToolChoice = "none"
	case "ANY":
		r.ToolChoice = "required"
		if len(allowed) == 1 && len(r.Tools) == 1 && r.Tools[0].Name == allowed[0] {
			r.ToolChoice = "name:" + allowed[0]
		}
	case "AUTO", "VALIDATED":
		r.ToolChoice = "auto"
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") {
			r.JSON = true
			schema := gc.ResponseJSONSchema
			if len(bytes.TrimSpace(schema)) == 0 || string(bytes.TrimSpace(schema)) == "null" {
				schema = gc.ResponseSchema
			}
			r.JSONSchema = geminiSchema(schema, false)
		}
		if tc := gc.ThinkingConfig; tc != nil {
			if tc.IncludeThoughts {
				r.Thinking = true
			}
			if tc.ThinkingBudget != nil {
				switch b := *tc.ThinkingBudget; {
				case b < 0:
					r.Effort, r.Thinking = "medium", true
				case b == 0:
					r.Effort = "low"
				default:
					r.Effort, r.Thinking = effortOfBudget(b), true
				}
			}
			switch strings.ToLower(tc.ThinkingLevel) {
			case "minimal", "low":
				r.Effort, r.Thinking = "low", true
			case "medium":
				r.Effort, r.Thinking = "medium", true
			case "high":
				r.Effort, r.Thinking = "high", true
			}
		}
	}
	return r, nil
}

// gResponseText flattens a functionResponse.response object to the text a
// tool result carries; Gemini CLI wraps output as {"output":…} or {"error":…}.
func gResponseText(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && len(m) == 1 {
		for k, v := range m {
			if k == "output" || k == "error" {
				var s string
				if json.Unmarshal(v, &s) != nil {
					s = string(v)
				}
				return s, k == "error"
			}
		}
	}
	return string(raw), false
}

// Schema keys whose value is a name→schema map, or data to leave alone.
var (
	schemaMaps = map[string]bool{"properties": true, "patternProperties": true, "$defs": true, "definitions": true}
	schemaData = map[string]bool{"default": true, "example": true, "examples": true, "enum": true, "const": true}
	schemaInts = map[string]bool{"minItems": true, "maxItems": true, "minLength": true, "maxLength": true,
		"minProperties": true, "maxProperties": true}
)

// geminiSchema turns a Gemini schema (upper-case type names, nullable,
// numbers as strings) into plain JSON Schema. A tool's parameters are
// made an object schema when they say so only by their properties.
func geminiSchema(raw json.RawMessage, tool bool) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	v = normSchema(v)
	if m, ok := v.(map[string]any); ok && tool {
		if _, has := m["type"]; !has {
			if _, props := m["properties"]; props || len(m) == 0 {
				m["type"] = "object"
			}
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

func normSchema(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = normSchema(x[i])
		}
		return x
	case map[string]any:
		for k, val := range x {
			switch {
			case schemaData[k]:
			case schemaMaps[k]:
				if sub, ok := val.(map[string]any); ok {
					for name, s := range sub {
						sub[name] = normSchema(s)
					}
				}
			case k == "type":
				switch t := val.(type) {
				case string:
					x[k] = strings.ToLower(t)
				case []any:
					for i := range t {
						if s, ok := t[i].(string); ok {
							t[i] = strings.ToLower(s)
						}
					}
				}
			case k == "nullable":
				if _, ok := val.(bool); ok {
					delete(x, k)
				}
			case k == "propertyOrdering":
				delete(x, k)
			case k == "format":
				if s, ok := val.(string); ok && s == "enum" {
					delete(x, k)
				}
			case schemaInts[k]:
				if s, ok := val.(string); ok {
					if n, err := strconv.ParseInt(s, 10, 64); err == nil {
						x[k] = n
					}
				}
			default:
				x[k] = normSchema(val)
			}
		}
		return x
	}
	return v
}

// ---- reply ---------------------------------------------------------------------

func finishToGemini(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// gemini is usage in Gemini's counts: the prompt includes cached tokens,
// thinking is counted apart from the candidate's own output.
func (u Usage) gemini() map[string]any {
	prompt := u.Input + u.CacheRead + u.CacheWrite
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	m := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand, "totalTokenCount": prompt + cand}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	return m
}

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
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}}
	}
	return map[string]any{"text": p.Text}
}

func geminiResponse(parts []map[string]any, finish string, usage map[string]any, id, model string) map[string]any {
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

// geminiEncoder writes events as a Gemini SSE stream. Text and thinking go
// out as they come; a function call waits until its arguments are whole.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector
	tool  *Part // the open call
	args  strings.Builder
}

func (e *geminiEncoder) respID() string {
	if e.id == "" {
		e.id = newID()
	}
	return e.id
}

func (e *geminiEncoder) send(part map[string]any) {
	e.w.event("", geminiResponse([]map[string]any{part}, "", nil, e.respID(), e.model))
}

func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	p := *e.tool
	p.Args = parseArgs(e.args.String())
	e.tool = nil
	e.args.Reset()
	e.send(geminiPart(p))
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text, "thought": true})
		}
	case KSig:
		if ev.Text != "" {
			e.send(map[string]any{"text": "", "thought": true, "thoughtSignature": ev.Text})
		}
	case KToolStart:
		e.flushTool()
		e.tool = &Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushTool()
	case KError:
		e.w.event("", map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	res := e.col.finish()
	e.w.event("", geminiResponse(nil, finishToGemini(res.Stop), res.Usage.gemini(), e.respID(), e.model))
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	var parts []map[string]any
	for _, p := range res.Parts {
		if p.Kind == Text && p.Text == "" {
			continue
		}
		parts = append(parts, geminiPart(p))
	}
	if len(parts) == 0 {
		parts = []map[string]any{{"text": ""}}
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	if res.Model != "" {
		model = res.Model
	}
	b, _ := json.Marshal(geminiResponse(parts, finishToGemini(res.Stop), res.Usage.gemini(), id, model))
	return b
}

// ---- HTTP -----------------------------------------------------------------------

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
	case 501:
		return "UNIMPLEMENTED"
	case 502, 503, 529:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	return "INTERNAL"
}

func geminiModelObject(e provider.Entry) map[string]any {
	return map[string]any{"name": "models/" + e.ID, "baseModelId": e.ID, "version": "dial", "displayName": e.Name,
		"description":                e.Name + " via dial (" + e.Provider.Name + ")",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}

// geminiToken is the key a Gemini client sent: x-goog-api-key, ?key=, or a
// bearer token. The gateway is loopback-only and accepts any of them.
func geminiToken(r *http.Request) string {
	if v := r.Header.Get("x-goog-api-key"); v != "" {
		return v
	}
	if v := r.URL.Query().Get("key"); v != "" {
		return v
	}
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

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
	writeError(w, provider.Gemini, 404, "unknown model "+id)
}

// geminiCall serves {model}:{method} calls.
func (s *Server) geminiCall(w http.ResponseWriter, r *http.Request) {
	_ = geminiToken(r)
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, provider.Gemini, 404, "expected /v1beta/models/{model}:generateContent, :streamGenerateContent or :countTokens")
		return
	}
	model, method := rest[:i], rest[i+1:]
	if method != "generateContent" && method != "streamGenerateContent" && method != "countTokens" {
		writeError(w, provider.Gemini, 404, "unknown method "+method)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	var m map[string]json.RawMessage
	if len(bytes.TrimSpace(body)) == 0 {
		m = map[string]json.RawMessage{}
	} else if err := json.Unmarshal(body, &m); err != nil || m == nil {
		writeError(w, provider.Gemini, 400, "invalid request body")
		return
	}
	if raw, ok := m["generateContentRequest"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(raw, &inner) == nil && inner != nil {
			m = inner
		}
	}
	m["model"], _ = json.Marshal(model)
	m["stream"], _ = json.Marshal(method == "streamGenerateContent")
	body, _ = json.Marshal(m)
	if method == "countTokens" {
		s.geminiCount(w, body)
		return
	}
	s.serve(w, r, provider.Gemini, body)
}

func (s *Server) geminiCount(w http.ResponseWriter, body []byte) {
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
			if p.Kind == Image {
				n += 258 * 4
			}
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	writeJSON(w, 200, map[string]any{"totalTokens": (n + 3) / 4})
}

