package gateway

// Gemini generateContent as a client protocol. Gemini clients (the Gemini CLI
// and anything else that speaks the Gemini API) point at the gateway and name
// any catalog model in the URL. The request is parsed into the shared Request
// form and goes to whichever provider serves the model, exactly as the other
// client APIs do. Gemini is never an upstream protocol.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// geminiMethods are the methods served under /v1beta/models/{model}:{method}.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// ---- request ------------------------------------------------------------------

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFileData struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gFnCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gFnResp struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gPart struct {
	Text             *string    `json:"text"`
	Thought          bool       `json:"thought"`
	InlineData       *gBlob     `json:"inlineData"`
	FileData         *gFileData `json:"fileData"`
	FunctionCall     *gFnCall   `json:"functionCall"`
	FunctionResponse *gFnResp   `json:"functionResponse"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gFunc struct {
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
	Model             string     `json:"model"`  // put there by the gateway from the URL
	Stream            bool       `json:"stream"` // put there by the gateway from the method
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Tools             []struct {
		FunctionDeclarations []gFunc `json:"functionDeclarations"`
	} `json:"tools"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *gGenConfig `json:"generationConfig"`
}

// geminiEnvelope adds the model and stream flag the URL carries to the JSON
// body, so the body alone describes the request.
func geminiEnvelope(body []byte, model string, stream bool) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	mb, _ := json.Marshal(model)
	sb, _ := json.Marshal(stream)
	m["model"], m["stream"] = mb, sb
	return json.Marshal(m)
}

func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}

	// Gemini function calls may come without ids. A response without an id
	// pairs with the most recent call of the same name.
	latest := map[string]string{}
	n := 0
	newCallID := func() string {
		n++
		return fmt.Sprintf("call_%s_%d", newID(), n)
	}
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
					id = newCallID()
				}
				latest[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: gemArgs(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					if id = latest[fr.Name]; id == "" {
						id = newCallID()
					}
				}
				text, isErr := gemResponseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			case p.InlineData != nil:
				if isImageType(p.InlineData.MimeType) {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[inline attachment of type %s omitted]", p.InlineData.MimeType)})
				}
			case p.FileData != nil:
				if isImageType(p.FileData.MimeType) {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file %s (%s)]", p.FileData.FileURI, p.FileData.MimeType)})
				}
			case p.Text != nil && *p.Text != "":
				kind := Text
				if p.Thought {
					kind = Thinking
				}
				msg.Parts = append(msg.Parts, Part{Kind: kind, Text: *p.Text})
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}

	var sys []string
	if g.SystemInstruction != nil {
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != nil && *p.Text != "" {
				sys = append(sys, *p.Text)
			}
		}
	}
	r.System = strings.Join(sys, "\n\n")

	for _, t := range g.Tools {
		for _, f := range t.FunctionDeclarations {
			schema := f.ParametersJSONSchema
			if len(schema) == 0 {
				schema = f.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: lowerSchema(schema)})
		}
	}

	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		case "ANY":
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if t := gc.ThinkingConfig; t != nil {
			r.Effort = geminiEffort(t.ThinkingBudget, t.ThinkingLevel)
			r.Thinking = t.IncludeThoughts || r.Effort != ""
		}
		if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			// upstreams have no JSON mode to ask for; say it in the system prompt
			instr := jsonInstruction(lowerSchema(schema))
			if r.System == "" {
				r.System = instr
			} else {
				r.System += "\n\n" + instr
			}
		}
	}
	return r, nil
}

// gemArgs is a function call's arguments as a JSON object, never empty.
func gemArgs(raw json.RawMessage) json.RawMessage {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

// gemResponseText reads a function response: an "error" is an error result,
// an "output" string is the result text, anything else is sent as JSON.
func gemResponseText(raw json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if e, ok := m["error"]; ok {
			return stringOrJSON(e), true
		}
		if o, ok := m["output"]; ok {
			return stringOrJSON(o), false
		}
	}
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return "", false
	}
	return string(raw), false
}

func stringOrJSON(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func isImageType(mime string) bool {
	return strings.HasPrefix(strings.ToLower(mime), "image/")
}

// geminiEffort maps thinkingConfig to a reasoning effort. A budget of -1 lets
// the model decide, which is treated as medium; 0 turns thinking off.
func geminiEffort(budget *int, level string) string {
	if budget != nil {
		switch n := *budget; {
		case n < 0:
			return "medium"
		case n == 0:
			return ""
		default:
			return effortOfBudget(n)
		}
	}
	return effortOf(level)
}

// lowerSchema rewrites Gemini's upper-case schema types (OBJECT, STRING, …) to
// the lower-case names JSON Schema uses, and drops Gemini-only ordering hints.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(lowerTypes(v))
	if err != nil {
		return raw
	}
	return out
}

func lowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "propertyOrdering")
		for k, val := range x {
			if k == "type" {
				switch t := val.(type) {
				case string:
					x[k] = strings.ToLower(t)
					continue
				case []any:
					for i := range t {
						if s, ok := t[i].(string); ok {
							t[i] = strings.ToLower(s)
						}
					}
					continue
				}
			}
			x[k] = lowerTypes(val)
		}
	case []any:
		for i := range x {
			x[i] = lowerTypes(x[i])
		}
	}
	return v
}

func jsonInstruction(schema json.RawMessage) string {
	s := "Respond only with a single JSON value. Do not add prose, markdown fences or commentary."
	if len(schema) > 0 {
		s += " The JSON must conform to this JSON schema:\n" + string(schema)
	}
	return s
}

// ---- response -----------------------------------------------------------------

func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// usageGemini is the usageMetadata block. Output counts every completion
// token, reasoning included, as the other APIs' renderers do.
func usageGemini(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	return map[string]any{
		"promptTokenCount":        prompt,
		"candidatesTokenCount":    u.Output,
		"totalTokenCount":         prompt + u.Output,
		"thoughtsTokenCount":      u.Reasoning,
		"cachedContentTokenCount": u.CacheRead,
	}
}

// renderGemini is the whole reply for a non-streaming Gemini client.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text, "thought": true})
			}
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
		id = "resp_" + newID()
	}
	mv := res.Model
	if mv == "" {
		mv = model
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": stopToGemini(res.Stop),
			"index":        0,
		}},
		"usageMetadata": usageGemini(res.Usage),
		"modelVersion":  mv,
		"responseId":    id,
	})
	return b
}

// geminiEncoder writes a streamed reply: one SSE event per part, in the order
// the reply produced them, then a final event with finishReason and usage.
// A function call is held until its arguments are complete.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	usage Usage
	stop  string
	tool  *gemTool
}

type gemTool struct {
	id, name string
	args     strings.Builder
}

func (e *geminiEncoder) emit(part map[string]any) {
	e.w.event("", map[string]any{"candidates": []map[string]any{{
		"content": map[string]any{"role": "model", "parts": []any{part}},
		"index":   0,
	}}})
}

func (e *geminiEncoder) flushTool() {
	t := e.tool
	if t == nil {
		return
	}
	e.tool = nil
	id := t.id
	if id == "" {
		id = "call_" + newID()
	}
	e.emit(map[string]any{"functionCall": map[string]any{"id": id, "name": t.name, "args": parseArgs(t.args.String())}})
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
		e.usage.add(ev.Usage)
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.emit(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.emit(map[string]any{"text": ev.Text, "thought": true})
		}
	case KToolStart:
		e.flushTool()
		e.tool = &gemTool{id: ev.ID, name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.tool.args.WriteString(ev.Text)
		}
	case KStop:
		e.stop = ev.Stop
	case KUsage:
		e.usage.add(ev.Usage)
	case KError:
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	if e.id == "" {
		e.id = "resp_" + newID()
	}
	mv := e.model
	e.w.event("", map[string]any{"candidates": []map[string]any{{
		"content":      map[string]any{"role": "model", "parts": []any{}},
		"finishReason": stopToGemini(e.stop),
		"index":        0,
	}}, "usageMetadata": usageGemini(e.usage), "modelVersion": mv, "responseId": e.id})
}

// ---- errors -------------------------------------------------------------------

// geminiStatus is the gRPC-style status name Gemini clients expect.
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
	case 500:
		return "INTERNAL"
	}
	if code >= 500 {
		return "UNAVAILABLE"
	}
	return "UNKNOWN"
}

// ---- handlers -----------------------------------------------------------------

// gemini serves POST /v1beta/models/{target}, where target is "<model>:<method>".
// The model is a catalog reference and may itself contain colons, so the method
// is whatever follows the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	target := r.PathValue("target")
	i := strings.LastIndex(target, ":")
	if i < 0 || !isGeminiMethod(target[i+1:]) {
		writeError(w, provider.Gemini, 404, fmt.Sprintf("dial serves the Gemini methods %s under /v1beta/models/{model}", strings.Join(geminiMethods, ", ")))
		return
	}
	model, method := target[:i], target[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	if method == "countTokens" {
		s.geminiCount(w, model, body)
		return
	}
	env, err := geminiEnvelope(body, model, method == "streamGenerateContent")
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	s.serve(w, r, provider.Gemini, env)
}

func isGeminiMethod(m string) bool {
	for _, g := range geminiMethods {
		if g == m {
			return true
		}
	}
	return false
}

// geminiCount estimates the tokens of a request, which may be given bare or
// wrapped in generateContentRequest. The estimate is characters over four.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, provider.Gemini, 404, fmt.Sprintf("dial knows no model %q", model))
		return
	}
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 {
		body = wrap.GenerateContentRequest
	}
	env, err := geminiEnvelope(body, model, false)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	req, err := parseGemini(env)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
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
	writeJSON(w, http.StatusOK, map[string]any{"totalTokens": n / 4})
}

// geminiList lists the catalog for Gemini clients, named models/<reference>.
func (s *Server) geminiList(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, map[string]any{
			"name":                       "models/" + e.ID,
			"displayName":                e.Name,
			"supportedGenerationMethods": geminiMethods,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}
