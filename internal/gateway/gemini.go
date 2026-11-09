package gateway

// Google's Gemini generateContent API, served as a client protocol: Gemini
// CLI and other Gemini clients point at the gateway, and their requests are
// translated into the gateway's common form like any other client API. No
// provider is spoken to in Gemini; the upstream side is always Chat,
// Responses or Anthropic.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// geminiMethods are the generation methods the model list advertises.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// ---- request ----------------------------------------------------------------

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFile struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type gPart struct {
	Text             *string    `json:"text,omitempty"`
	Thought          bool       `json:"thought,omitempty"`
	ThoughtSignature string     `json:"thoughtSignature,omitempty"`
	InlineData       *gBlob     `json:"inlineData,omitempty"`
	FileData         *gFile     `json:"fileData,omitempty"`
	FunctionCall     *gCall     `json:"functionCall,omitempty"`
	FunctionResponse *gResponse `json:"functionResponse,omitempty"`
}

type gContent struct {
	Role  string  `json:"role,omitempty"`
	Parts []gPart `json:"parts"`
}

type gRequest struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction,omitempty"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name                 string          `json:"name"`
			Description          string          `json:"description,omitempty"`
			Parameters           json.RawMessage `json:"parameters,omitempty"`
			ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
		} `json:"functionDeclarations"`
	} `json:"tools,omitempty"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig,omitempty"`
	GenerationConfig *struct {
		MaxOutputTokens    int             `json:"maxOutputTokens"`
		Temperature        *float64        `json:"temperature"`
		TopP               *float64        `json:"topP"`
		StopSequences      []string        `json:"stopSequences"`
		ResponseMimeType   string          `json:"responseMimeType"`
		ResponseSchema     json.RawMessage `json:"responseSchema,omitempty"`
		ResponseJSONSchema json.RawMessage `json:"responseJsonSchema,omitempty"`
		ThinkingConfig     *struct {
			IncludeThoughts bool   `json:"includeThoughts"`
			ThinkingBudget  *int   `json:"thinkingBudget"`
			ThinkingLevel   string `json:"thinkingLevel"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig,omitempty"`
	// Stream is not part of Gemini's body; the route sets it, because the
	// method name (streamGenerateContent) is what says the client streams.
	Stream bool `json:"stream"`
}

// parseGemini reads a generateContent body into the common request form.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Stream: g.Stream}
	var sys []string
	if g.SystemInstruction != nil {
		if s := geminiText(g.SystemInstruction.Parts); s != "" {
			sys = append(sys, s)
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			sys = append(sys, jsonModeInstruction(gc.ResponseJSONSchema, gc.ResponseSchema))
		}
		if tc := gc.ThinkingConfig; tc != nil {
			r.Effort = geminiEffort(tc.ThinkingBudget, tc.ThinkingLevel)
			r.Thinking = tc.IncludeThoughts || r.Effort != ""
		}
	}
	r.System = strings.Join(sys, "\n\n")

	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.ParametersJSONSchema
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: lowerSchema(schema)})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		default: // AUTO, VALIDATED, or unset
			r.ToolChoice = "auto"
		}
	}

	// Calls without an id are paired with later responses by function name:
	// a response without an id answers the most recent call of its name.
	latest := map[string]string{}
	ncalls := 0
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
					ncalls++
					id = fmt.Sprintf("call_%s_%d", newID(), ncalls)
				}
				latest[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: objectOf(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = latest[fr.Name]
				}
				if id == "" {
					id = "call_" + newID()
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: geminiResponseText(fr.Response)})
			case p.Text != nil:
				if *p.Text == "" {
					continue
				}
				if p.Thought {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: *p.Text, Signature: p.ThoughtSignature})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
				}
			case p.InlineData != nil:
				mt, data := p.InlineData.MimeType, p.InlineData.Data
				if strings.HasPrefix(mt, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, Data: data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[inline %s attachment, %d base64 characters, not forwarded]", mt, len(data))})
				}
			case p.FileData != nil:
				mt, uri := p.FileData.MimeType, p.FileData.FileURI
				if strings.HasPrefix(mt, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, URL: uri})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file %s (%s), not forwarded]", uri, mt)})
				}
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}
	return r, nil
}

// geminiText joins the text parts of a content.
func geminiText(parts []gPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text != nil && !p.Thought {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}

// objectOf is a JSON object from raw, or {} when there is none.
func objectOf(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// geminiResponseText is a function response as the text an upstream
// expects. A response with one string field gives that string.
func geminiResponseText(raw json.RawMessage) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && len(m) == 1 {
		for _, v := range m {
			var s string
			if json.Unmarshal(v, &s) == nil {
				return s
			}
		}
	}
	return string(objectOf(raw))
}

// geminiEffort maps a Gemini thinking budget or level to an effort. A
// budget of -1 lets the model decide, which is treated as medium.
func geminiEffort(budget *int, level string) string {
	if budget != nil {
		switch n := *budget; {
		case n < 0:
			return "medium"
		case n <= 1024:
			return "low"
		case n <= 8192:
			return "medium"
		default:
			return "high"
		}
	}
	switch strings.ToLower(level) {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	}
	return ""
}

// jsonModeInstruction asks for JSON in the system prompt, since the
// upstream protocols have no response-format field we can rely on here.
func jsonModeInstruction(schemas ...json.RawMessage) string {
	var b strings.Builder
	b.WriteString("Respond only with JSON. Output nothing but a single valid JSON value.")
	for _, s := range schemas {
		if l := lowerSchema(s); l != nil {
			b.WriteString("\nThe JSON must conform to this JSON schema:\n")
			b.Write(l)
			break
		}
	}
	return b.String()
}

// lowerSchema turns Gemini's upper-case schema types (OBJECT, STRING…)
// into the lower-case JSON Schema names. nil stands for no schema.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
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
		for k, val := range x {
			if s, ok := val.(string); ok && k == "type" {
				x[k] = strings.ToLower(s)
				continue
			}
			x[k] = lowerTypes(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = lowerTypes(x[i])
		}
		return x
	}
	return v
}

// ---- reply ------------------------------------------------------------------

// geminiFinish maps a stop reason to Gemini's finishReason. A reply that
// calls functions also finishes with STOP, as Gemini does.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

func geminiUsage(u Usage) map[string]any {
	in := u.Input + u.CacheRead
	return map[string]any{
		"promptTokenCount":     in,
		"candidatesTokenCount": u.Output,
		"thoughtsTokenCount":   u.Reasoning,
		"totalTokenCount":      in + u.Output,
	}
}

// geminiCallPart is a function call as a Gemini part.
func geminiCallPart(p Part) map[string]any {
	fc := map[string]any{"name": p.Name, "args": argsOf(p)}
	if p.ID != "" {
		fc["id"] = p.ID
	}
	return map[string]any{"functionCall": fc}
}

// renderGemini is the whole reply as one generateContent response.
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
			parts = append(parts, geminiCallPart(p))
		}
	}
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
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

// geminiEncoder writes events as a generateContent SSE stream: one event
// per part, in the order the reply produced them, then a final event with
// the finish reason and usage. A function call is sent once its arguments
// are complete, which is when the next part (or the end) begins.
type geminiEncoder struct {
	w     *sseWriter
	model string
	col   collector
}

func (e *geminiEncoder) send(parts ...map[string]any) {
	e.w.event("", map[string]any{"candidates": []map[string]any{{
		"content": map[string]any{"role": "model", "parts": parts},
	}}})
}

// flushTool sends the open function call, if any, and closes it.
func (e *geminiEncoder) flushTool() {
	n := len(e.col.res.Parts)
	if n == 0 {
		return
	}
	if p := e.col.res.Parts[n-1]; p.Kind != ToolCall || p.Args != nil {
		return
	}
	e.col.closeTool()
	e.send(geminiCallPart(e.col.res.Parts[n-1]))
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart, KSig, KToolArgs, KUsage:
		e.col.add(ev)
		return
	case KError:
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": geminiStatus(502)}})
		e.col.add(ev)
		return
	}
	e.flushTool()
	switch ev.Kind {
	case KText:
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text})
		}
	case KThink:
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text, "thought": true})
		}
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	res := e.col.finish()
	id := res.ID
	if id == "" {
		id = newID()
	}
	model := res.Model
	if model == "" {
		model = e.model
	}
	e.w.event("", map[string]any{"candidates": []map[string]any{{
		"content":      map[string]any{"role": "model", "parts": []any{}},
		"finishReason": geminiFinish(res.Stop),
		"index":        0,
	}}, "usageMetadata": geminiUsage(res.Usage), "modelVersion": model, "responseId": id})
}

// geminiStatus is the gRPC-style status name Gemini gives an HTTP code.
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
	case code == 504:
		return "DEADLINE_EXCEEDED"
	case code == 502 || code == 503 || code == 529:
		return "UNAVAILABLE"
	case code >= 500:
		return "INTERNAL"
	}
	return "UNKNOWN"
}

// ---- routes -----------------------------------------------------------------

// withStream sets the stream flag of a Gemini body from the route.
func withStream(body []byte, on bool) []byte {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	if on {
		m["stream"] = json.RawMessage("true")
	} else {
		m["stream"] = json.RawMessage("false")
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// gemini serves POST /v1beta/models/{rest}, where rest is "<model>:<method>".
// The model is the catalog reference, which may itself contain slashes and
// colons; the method is whatever follows the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, provider.Gemini, 404, "dial serves the Gemini API at /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	model, method := rest[:i], rest[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	switch method {
	case "generateContent":
		s.serve(w, r, provider.Gemini, withStream(body, false), model)
	case "streamGenerateContent":
		s.serve(w, r, provider.Gemini, withStream(body, true), model)
	case "countTokens":
		s.geminiCount(w, body, model)
	default:
		writeError(w, provider.Gemini, 404, fmt.Sprintf("%s is not a method of the Gemini API dial serves", method))
	}
}

// geminiCount estimates the tokens of a request, which may be given bare
// or inside generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, body []byte, model string) {
	var q struct {
		Inner json.RawMessage `json:"generateContentRequest"`
	}
	json.Unmarshal(body, &q)
	if len(q.Inner) > 0 {
		body = q.Inner
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, provider.Gemini, 404, unknownModelMsg(model))
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
	writeJSON(w, 200, map[string]any{"totalTokens": n / 4})
}

// geminiModelObject is one model in the Gemini model list.
func geminiModelObject(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.Model
	}
	return map[string]any{"name": "models/" + e.ID, "displayName": name, "description": name,
		"supportedGenerationMethods": geminiMethods}
}

// geminiModels lists the catalog in the Gemini shape.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiModel answers GET /v1beta/models/{id} for one catalog model.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, "unknown model "+id)
}

// unknownModelMsg explains which models the gateway would have answered.
func unknownModelMsg(model string) string {
	msg := fmt.Sprintf("dial knows no model %q", model)
	if ids := provider.IDs(); len(ids) > 0 {
		msg += "; it has " + strings.Join(ids, ", ")
	} else {
		msg += "; add a provider in dial first"
	}
	return msg
}
