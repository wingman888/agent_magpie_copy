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

// ---- Google Gemini API --------------------------------------------------------
//
// Gemini is only ever a client-facing API: no upstream speaks it, so a
// Gemini call is always translated to one of the three provider protocols.

// Gemini is the client protocol of the Gemini API.
const Gemini = provider.Gemini

type gPart struct {
	Text             string          `json:"text,omitempty"`
	Thought          bool            `json:"thought,omitempty"`
	ThoughtSignature string          `json:"thoughtSignature,omitempty"`
	InlineData       *gBlob          `json:"inlineData,omitempty"`
	FileData         *gFile          `json:"fileData,omitempty"`
	FunctionCall     *gFunctionCall  `json:"functionCall,omitempty"`
	FunctionResponse *gFunctionReply `json:"functionResponse,omitempty"`
}

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

type gFunctionReply struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
	Parts    []gPart         `json:"parts,omitempty"`
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
			IncludeThoughts *bool  `json:"includeThoughts,omitempty"`
		} `json:"thinkingConfig,omitempty"`
	} `json:"generationConfig,omitempty"`
}

// parseGemini reads a GenerateContentRequest. The model and whether to
// stream come from the URL, so the caller fills them in.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{System: geminiSystem(g.SystemInstruction)}

	// tool call ids: kept when the client sent them, made up otherwise; a
	// response without an id answers the latest call of the same name
	type call struct {
		id, name string
		answered bool
	}
	var calls []*call
	n := 0
	newCallID := func() string {
		n++
		return fmt.Sprintf("call_%d_%s", n, newID())
	}
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" || c.Role == "assistant" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					id = newCallID()
				}
				calls = append(calls, &call{id: id, name: p.FunctionCall.Name})
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: geminiArgs(p.FunctionCall.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id != "" {
					for _, c := range calls {
						if c.id == id {
							c.answered = true
						}
					}
				} else {
					var hit *call
					for i := len(calls) - 1; i >= 0; i-- {
						if calls[i].name == fr.Name && !calls[i].answered {
							hit = calls[i]
							break
						}
					}
					if hit == nil {
						for i := len(calls) - 1; i >= 0; i-- {
							if calls[i].name == fr.Name {
								hit = calls[i]
								break
							}
						}
					}
					if hit != nil {
						hit.answered = true
						id = hit.id
					} else {
						id = newCallID()
					}
				}
				out, isErr := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: isErr})
				for _, sub := range fr.Parts {
					if q, ok := geminiMedia(sub); ok {
						msg.Parts = append(msg.Parts, q)
					}
				}
			case p.Thought:
				if p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text})
				}
			case p.InlineData != nil || p.FileData != nil:
				if q, ok := geminiMedia(p); ok {
					msg.Parts = append(msg.Parts, q)
				}
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}
	r.Messages = mergeTurns(r.Messages)

	var allowed []string
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		allowed = fc.AllowedFunctionNames
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			if len(allowed) == 1 {
				r.ToolChoice = "name:" + allowed[0]
			} else {
				r.ToolChoice = "required"
			}
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		}
	}
	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			if fd.Name == "" {
				continue
			}
			if len(allowed) > 0 && r.ToolChoice != "none" && !slices.Contains(allowed, fd.Name) {
				continue
			}
			schema := fd.ParametersJSONSchema
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: normalizeSchema(schema)})
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if mt := strings.ToLower(strings.TrimSpace(gc.ResponseMimeType)); mt == "application/json" || strings.HasSuffix(mt, "+json") {
			r.JSON = true
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			r.JSONSchema = normalizeSchema(schema)
		}
		if tc := gc.ThinkingConfig; tc != nil {
			if tc.ThinkingBudget != nil {
				switch b := *tc.ThinkingBudget; {
				case b < 0:
					r.Thinking, r.Effort = true, "medium"
				case b == 0:
					r.Thinking, r.Effort = false, ""
				default:
					r.Thinking, r.Budget = true, b
					switch {
					case b <= 1024:
						r.Effort = "low"
					case b <= 8192:
						r.Effort = "medium"
					default:
						r.Effort = "high"
					}
				}
			}
			if l := strings.ToLower(strings.TrimSpace(tc.ThinkingLevel)); l != "" {
				if e := effortOf(l); e != "" {
					r.Thinking, r.Effort, r.Budget = true, e, 0
				}
			}
			if tc.IncludeThoughts != nil {
				if *tc.IncludeThoughts {
					if tc.ThinkingBudget == nil || *tc.ThinkingBudget != 0 {
						r.Thinking = true
					}
				} else {
					r.HideThoughts = true
				}
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
	json.Unmarshal(raw, &c)
	var out []string
	for _, p := range c.Parts {
		if p.Text != "" && !p.Thought {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// geminiMedia turns inline or linked data into an image, or a placeholder
// for anything an upstream could not take.
func geminiMedia(p gPart) (Part, bool) {
	switch {
	case p.InlineData != nil:
		mt := strings.ToLower(p.InlineData.MimeType)
		if strings.HasPrefix(mt, "image/") && p.InlineData.Data != "" {
			return Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data}, true
		}
		if mt == "" {
			mt = "unknown"
		}
		return Part{Kind: Text, Text: "[attachment: " + mt + " omitted]"}, true
	case p.FileData != nil:
		mt := strings.ToLower(p.FileData.MimeType)
		if strings.HasPrefix(mt, "image/") && strings.HasPrefix(p.FileData.FileURI, "http") {
			return Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI}, true
		}
		if mt == "" {
			mt = "file"
		}
		return Part{Kind: Text, Text: "[attachment: " + mt + " omitted]"}, true
	case p.Text != "":
		return Part{Kind: Text, Text: p.Text}, true
	}
	return Part{}, false
}

// geminiArgs keeps a function call's arguments as a JSON object.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return parseArgs(s)
}

// geminiResult flattens a functionResponse.response into the text of a
// tool result: {"output": "…"} becomes the string, {"error": …} marks it
// failed, anything else stays JSON.
func geminiResult(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if len(m) == 1 {
			for k, v := range m {
				var s string
				if (k == "output" || k == "error" || k == "result" || k == "content") && json.Unmarshal(v, &s) == nil {
					return s, k == "error"
				}
				if k == "error" {
					return string(v), true
				}
			}
		}
		if _, ok := m["error"]; ok {
			if _, out := m["output"]; !out {
				return string(raw), true
			}
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	return string(raw), false
}

// normalizeSchema turns Gemini's OpenAPI-flavoured schema (upper-case type
// names) into plain JSON Schema.
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(normalizeSchemaValue(v))
	if err != nil {
		return raw
	}
	return out
}

var schemaTypes = map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "array": true, "object": true, "null": true}

func normalizeSchemaValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch k {
			case "enum", "const", "default", "example", "examples", "required", "propertyOrdering":
				continue // data, not schema
			case "type":
				switch t := val.(type) {
				case string:
					l := strings.ToLower(t)
					if schemaTypes[l] {
						x[k] = l
					} else if l == "type_unspecified" || l == "" {
						delete(x, k)
					}
				case []any:
					for i, e := range t {
						if s, ok := e.(string); ok && schemaTypes[strings.ToLower(s)] {
							t[i] = strings.ToLower(s)
						}
					}
				default:
					x[k] = normalizeSchemaValue(val)
				}
			case "properties", "$defs", "definitions", "patternProperties":
				// a map of names to schemas; a property may be called "type"
				if m, ok := val.(map[string]any); ok {
					for name, s := range m {
						m[name] = normalizeSchemaValue(s)
					}
				}
			default:
				x[k] = normalizeSchemaValue(val)
			}
		}
		if n, ok := x["nullable"].(bool); ok {
			delete(x, "nullable")
			if t, ok := x["type"].(string); ok && n {
				x["type"] = []any{t, "null"}
			}
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = normalizeSchemaValue(e)
		}
		return x
	}
	return v
}

// jsonInstruction is what an upstream without a JSON mode is told when the
// client wants JSON back.
func jsonInstruction(r *Request) string {
	if !r.JSON {
		return ""
	}
	s := "Respond with only a single valid JSON value: no prose, no explanations, no Markdown code fences."
	if len(r.JSONSchema) > 0 {
		s += " The JSON must conform to this JSON Schema:\n" + string(r.JSONSchema)
	}
	return s
}

// withJSONInstruction is the system prompt with the JSON instruction added.
func withJSONInstruction(r *Request) string {
	ins := jsonInstruction(r)
	if ins == "" {
		return r.System
	}
	if r.System == "" {
		return ins
	}
	return r.System + "\n\n" + ins
}

// ---- replies --------------------------------------------------------------------

func finishToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

func (u Usage) gemini() map[string]any {
	prompt := u.Input + u.CacheRead + u.CacheWrite
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	out := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand, "totalTokenCount": prompt + u.Output}
	if u.Reasoning > 0 {
		out["thoughtsTokenCount"] = u.Reasoning
	}
	if u.CacheRead > 0 {
		out["cachedContentTokenCount"] = u.CacheRead
	}
	return out
}

func geminiCall(p Part) map[string]any {
	id := p.ID
	if id == "" {
		id = "call_" + newID()
	}
	return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": parseArgs(string(argsOf(p)))}}
}

func geminiResponseID(id string) string {
	if id == "" {
		return newID()
	}
	return id
}

// geminiEncoder writes events as Gemini stream chunks. Text and thoughts go
// out as they come; a function call waits until its arguments are whole.
type geminiEncoder struct {
	w     *sseWriter
	model string
	hide  bool // the client turned thoughts off
	col   collector
	sent  int // tool calls already written
	id    string
}

func (e *geminiEncoder) chunk(parts []map[string]any, finish string, u *Usage) {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	if e.id == "" {
		e.id = geminiResponseID(e.col.res.ID)
	}
	out := map[string]any{"candidates": []any{cand}, "modelVersion": e.model, "responseId": e.id}
	if u != nil {
		out["usageMetadata"] = u.gemini()
	}
	e.w.event("", out)
}

// flushTools writes the function calls whose arguments are complete.
func (e *geminiEncoder) flushTools() {
	i := 0
	for _, p := range e.col.res.Parts {
		if p.Kind != ToolCall {
			continue
		}
		if p.Args == nil {
			return
		}
		if i >= e.sent {
			e.chunk([]map[string]any{geminiCall(p)}, "", nil)
			e.sent++
		}
		i++
	}
}

func (e *geminiEncoder) event(ev Event) {
	e.col.add(ev)
	switch ev.Kind {
	case KText, KThink, KToolStart, KStop:
		e.flushTools()
	}
	switch ev.Kind {
	case KText:
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text}}, "", nil)
		}
	case KThink:
		if ev.Text != "" && !e.hide {
			e.chunk([]map[string]any{{"text": ev.Text, "thought": true}}, "", nil)
		}
	case KError:
		e.w.event("", map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
	}
}

func (e *geminiEncoder) finish() {
	res := e.col.finish()
	e.flushTools()
	// the closing chunk carries no content of its own, only why the turn
	// ended and what it cost; every call went out whole before it
	e.chunk([]map[string]any{}, finishToGemini(res.Stop), &res.Usage)
}

// geminiResponse is a whole GenerateContentResponse.
func geminiResponse(res Result, model string, hide bool) map[string]any {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			parts = append(parts, map[string]any{"text": p.Text})
		case Thinking:
			if !hide && p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text, "thought": true})
			}
		case ToolCall:
			parts = append(parts, geminiCall(p))
		}
	}
	return map[string]any{
		"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts},
			"finishReason": finishToGemini(res.Stop), "index": 0}},
		"usageMetadata": res.Usage.gemini(),
		"modelVersion":  model,
		"responseId":    geminiResponseID(res.ID),
	}
}

func renderGemini(res Result, model string, hide bool) []byte {
	b, _ := json.Marshal(geminiResponse(res, model, hide))
	return b
}

// geminiStatus is the google.rpc status name for an HTTP status.
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
	case 503, 529:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	if code >= 500 {
		return "INTERNAL"
	}
	return "FAILED_PRECONDITION"
}

// ---- HTTP ------------------------------------------------------------------------

// geminiTarget splits "models/fake/m1:generateContent" into the model and
// the method. Model ids may hold colons; the method follows the last one.
func geminiTarget(rest string) (model, method string) {
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, "/"), "models/")
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, ""
}

func geminiModel(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "baseModelId": e.ID, "version": "dial", "displayName": name,
		"description":                name + " via " + e.Provider.Name + ", served by dial",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}

func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModel(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

func (s *Server) geminiGet(w http.ResponseWriter, r *http.Request) {
	id, _ := geminiTarget(r.PathValue("rest"))
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModel(e))
			return
		}
	}
	writeError(w, Gemini, 404, fmt.Sprintf("dial knows no model %q", id))
}

// geminiPost serves generateContent, streamGenerateContent and countTokens.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	model, method := geminiTarget(r.PathValue("rest"))
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, Gemini, 400, err.Error())
		return
	}
	switch method {
	case "countTokens":
		s.geminiCount(w, body)
		return
	case "generateContent", "streamGenerateContent":
	default:
		writeError(w, Gemini, 404, fmt.Sprintf("dial does not serve method %q; it serves generateContent, streamGenerateContent and countTokens", method))
		return
	}
	if model == "" {
		writeError(w, Gemini, 400, "no model in the request path")
		return
	}
	start := time.Now()
	call := Call{Time: start, From: Gemini, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, upModel, ok := provider.Resolve(model)
	if !ok {
		s.unknownModel(w, Gemini, &call)
		return
	}
	req, err := parseGemini(body)
	if err != nil {
		call.Provider, call.Status, call.Error = p.ID, writeError(w, Gemini, 400, err.Error()), err.Error()
		s.record(call)
		return
	}
	req.Model = model
	req.Stream = method == "streamGenerateContent"
	req.arrayStream = req.Stream && r.URL.Query().Get("alt") != "sse"
	s.route(w, r, Gemini, &call, p, upModel, nil, req)
}

// geminiCount answers countTokens with a rough estimate.
func (s *Server) geminiCount(w http.ResponseWriter, body []byte) {
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 && string(wrap.GenerateContentRequest) != "null" {
		body = wrap.GenerateContentRequest
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, Gemini, 400, err.Error())
		return
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
			if p.Kind == Image {
				n += 1032 // about 258 tokens, Gemini's price of an image
			}
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	writeJSON(w, 200, map[string]any{"totalTokens": max(1, n/4)})
}
