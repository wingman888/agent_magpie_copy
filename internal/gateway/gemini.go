package gateway

// The Gemini API as a client protocol. Gemini CLI, and anything else that
// speaks the Gemini API, reaches the gateway here. Nothing in this file talks
// to an upstream: parseGemini turns a Gemini request into a Request that the
// upstream builders already understand, and the Gemini encoders turn a reply
// back into Gemini's shape. Upstreams keep speaking their own three protocols.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// gPart is one part of a Gemini content.
type gPart struct {
	Text             string `json:"text,omitempty"`
	Thought          bool   `json:"thought,omitempty"`
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
	InlineData       *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData,omitempty"`
	FileData *struct {
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

// gContent is one turn of a Gemini conversation.
type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

// gRequest is the part of a generateContent body dial reads.
type gRequest struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name                 string          `json:"name"`
			Description          string          `json:"description"`
			Parameters           json.RawMessage `json:"parameters"`
			ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
		} `json:"functionDeclarations"`
	} `json:"tools"`
	ToolConfig *struct {
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

// parseGemini reads a generateContent request. model is the catalog id from
// the URL path; stream says whether the call was streamGenerateContent.
func parseGemini(body []byte, model string, stream bool) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: model, Stream: stream}
	if g.SystemInstruction != nil {
		r.System = geminiPartsText(g.SystemInstruction.Parts)
	}

	// Gemini calls may lack ids. A result without an id is matched to the
	// most recent call of the same function name seen so far.
	lastID := map[string]string{}
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
				lastID[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiArgs(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = lastID[fr.Name]
				}
				if id == "" {
					id = "call_" + newID()
				}
				out, failed := geminiResponseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: failed})
			case p.Thought:
				if p.Text != "" || p.ThoughtSignature != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
				}
			case p.InlineData != nil:
				mt := p.InlineData.MimeType
				if strings.HasPrefix(mt, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[inline attachment omitted: " + mt + "]"})
				}
			case p.FileData != nil:
				mt, uri := p.FileData.MimeType, p.FileData.FileURI
				if strings.HasPrefix(mt, "image/") && uri != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, URL: uri})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[file omitted: " + uri + "]"})
				}
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
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

	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO":
			r.ToolChoice = "auto"
		case "ANY":
			// One allowed name pins the tool; several cannot be expressed
			// upstream, so they fall back to "must call some tool".
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if gc.ResponseMimeType == "application/json" || len(gc.ResponseSchema) > 0 || len(gc.ResponseJSONSchema) > 0 {
			// Upstreams have no JSON mode shared by all three protocols, so
			// the request asks for JSON in plain words, with the schema.
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			r.System = geminiJoinNonEmpty("\n\n", r.System, geminiJSONNote(geminiSchema(schema)))
		}
		if tc := gc.ThinkingConfig; tc != nil {
			switch {
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget == -1:
				r.Effort = "medium" // the model decides; treat as medium strength
			case tc.ThinkingBudget != nil:
				r.Effort = effortOfBudget(*tc.ThinkingBudget)
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			}
			r.Thinking = tc.IncludeThoughts || r.Effort != ""
		}
	}
	return r, nil
}

// geminiPartsText joins the text parts of a Gemini content.
func geminiPartsText(parts []gPart) string {
	var sys []string
	for _, p := range parts {
		if p.Text != "" && !p.Thought {
			sys = append(sys, p.Text)
		}
	}
	return strings.Join(sys, "\n\n")
}

func geminiJoinNonEmpty(sep string, parts ...string) string {
	var keep []string
	for _, p := range parts {
		if p != "" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, sep)
}

// geminiArgs is a function call's args as a JSON object, never empty.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return parseArgs(s)
}

// geminiResponseText reads a function response. An "error" field marks the
// result as failed; an "output" field is the text; anything else is passed
// on as the JSON it was.
func geminiResponseText(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return s, false
	}
	if e, ok := m["error"]; ok {
		return geminiStringOrRaw(e), true
	}
	if o, ok := m["output"]; ok {
		return geminiStringOrRaw(o), false
	}
	return s, false
}

func geminiStringOrRaw(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

// geminiJSONNote asks for JSON output in plain words, with the schema when
// there is one.
func geminiJSONNote(schema json.RawMessage) string {
	note := "Respond only with a single valid JSON value. Do not write any text, explanation or Markdown code fence outside the JSON."
	if len(schema) > 0 {
		note += "\nThe JSON must match this JSON schema:\n" + string(schema)
	}
	return note
}

// geminiSchema turns a Gemini schema into a standard lowercase JSON Schema.
// It returns nil for an absent or unreadable schema.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || v == nil {
		return nil
	}
	out, err := json.Marshal(geminiLowerSchema(v))
	if err != nil {
		return nil
	}
	return out
}

// geminiSchemaValueKeys hold plain JSON values, not schemas, so their
// contents are left alone.
var geminiSchemaValueKeys = map[string]bool{"enum": true, "const": true, "default": true, "example": true, "required": true}

// geminiLowerSchema lowercases schema type names recursively. Property names
// are data, not keywords, so they are kept as they are.
func geminiLowerSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, y := range x {
			switch {
			case k == "type":
				if t, ok := geminiLowerType(y); ok {
					out[k] = t
				}
			case geminiSchemaValueKeys[k]:
				out[k] = y
			case k == "properties" || k == "$defs" || k == "definitions" || k == "patternProperties":
				if pm, ok := y.(map[string]any); ok {
					props := make(map[string]any, len(pm))
					for pk, pv := range pm {
						props[pk] = geminiLowerSchema(pv)
					}
					out[k] = props
				} else {
					out[k] = y
				}
			default:
				out[k] = geminiLowerSchema(y)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, y := range x {
			out[i] = geminiLowerSchema(y)
		}
		return out
	}
	return v
}

func geminiLowerType(y any) (any, bool) {
	switch t := y.(type) {
	case string:
		if t == "TYPE_UNSPECIFIED" {
			return nil, false
		}
		return strings.ToLower(t), true
	case []any:
		var out []any
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, strings.ToLower(s))
			}
		}
		return out, true
	}
	return y, true
}

// geminiCountTokens estimates the tokens of a countTokens body, which is
// either a request or one wrapped in generateContentRequest. The estimate is
// about four characters per token, with a fixed charge per image.
func geminiCountTokens(body []byte) (int, error) {
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	raw := body
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 {
		raw = wrap.GenerateContentRequest
	}
	r, err := parseGemini(raw, "", false)
	if err != nil {
		return 0, err
	}
	n := len(r.System)
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			switch p.Kind {
			case Image:
				n += 258 * 4
			default:
				n += len(p.Text) + len(p.Args) + len(p.Name)
			}
		}
	}
	for _, t := range r.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return n / 4, nil
}

// ---- replies ------------------------------------------------------------------

// geminiBody is one GenerateContentResponse.
func geminiBody(id, model string, parts []any, finish string, usage map[string]any) map[string]any {
	if parts == nil {
		parts = []any{}
	}
	content := map[string]any{"role": "model", "parts": parts}
	cand := map[string]any{"content": content, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	out := map[string]any{"candidates": []any{cand}}
	if id != "" {
		out["responseId"] = id
	}
	if model != "" {
		out["modelVersion"] = model
	}
	if usage != nil {
		out["usageMetadata"] = usage
	}
	return out
}

// geminiFinish maps dial's stop reasons to Gemini's finish reasons.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage counts tokens the Gemini way. The prompt includes cache hits.
// Reasoning tokens are reported apart and are not part of the candidates.
func geminiUsage(u Usage) map[string]any {
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

// geminiCallPart is a function call as Gemini shows it. Calls are always
// whole: the arguments are complete by the time this is built. The second
// result is false when the arguments are not valid JSON (for example a
// stream cut off mid-object); such a call must not be sent. Arguments that
// are valid JSON but not an object are wrapped as {"input": ...}, as parseArgs
// does, because Gemini's args are always a Struct.
func geminiCallPart(p Part) (map[string]any, bool) {
	s := strings.TrimSpace(string(argsOf(p)))
	if !json.Valid([]byte(s)) {
		return nil, false
	}
	args := json.RawMessage(s)
	switch {
	case s == "null":
		args = json.RawMessage("{}")
	case !strings.HasPrefix(s, "{"):
		b, err := json.Marshal(map[string]json.RawMessage{"input": args})
		if err != nil {
			return nil, false
		}
		args = b
	}
	id := p.ID
	if id == "" {
		id = "call_" + newID()
	}
	return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": args}}, true
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	var parts []any
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
			if part, ok := geminiCallPart(p); ok {
				parts = append(parts, part)
			}
		}
	}
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	b, err := json.Marshal(geminiBody(id, model, parts, geminiFinish(res.Stop), geminiUsage(res.Usage)))
	if err != nil {
		// never an empty body: say what went wrong in Gemini's error shape
		b, _ = json.Marshal(map[string]any{"error": map[string]any{"code": 500, "message": "encode reply: " + err.Error(), "status": "INTERNAL"}})
	}
	return b
}

// geminiEncoder writes events as a Gemini SSE stream. Text and reasoning go
// out as they arrive. A function call goes out once its arguments are whole.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector
	tools int // function calls already sent
}

func newGeminiEncoder(w *sseWriter, model string) streamEncoder {
	return &geminiEncoder{w: w, model: model}
}

func (e *geminiEncoder) responseID() string {
	if e.id == "" {
		e.id = "resp_" + newID()
	}
	return e.id
}

// emit writes one event. An event that cannot be encoded is dropped rather
// than written as an empty data line.
func (e *geminiEncoder) emit(v map[string]any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	e.w.event("", json.RawMessage(b))
}

func (e *geminiEncoder) send(part map[string]any) {
	e.emit(geminiBody(e.responseID(), e.model, []any{part}, "", nil))
}

// flushTools sends the function calls that have closed, in order. A call
// still open blocks the ones after it, so no call is sent half-formed. A
// closed call whose arguments are invalid is skipped, but still counted so
// the calls after it are not blocked.
func (e *geminiEncoder) flushTools() {
	k := -1
	for _, p := range e.col.res.Parts {
		if p.Kind != ToolCall {
			continue
		}
		k++
		if k < e.tools {
			continue
		}
		if p.Args == nil {
			return
		}
		e.tools++
		if part, ok := geminiCallPart(p); ok {
			e.send(part)
		}
	}
}

func (e *geminiEncoder) event(ev Event) {
	if ev.Kind == KStart && ev.MsgID != "" {
		e.id = ev.MsgID
	}
	// Add first: an incoming text or reasoning delta closes an open call,
	// which must then go out before the delta does.
	e.col.add(ev)
	e.flushTools()
	switch ev.Kind {
	case KText:
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text})
		}
	case KThink:
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text, "thought": true})
		}
	case KError:
		e.emit(map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
	}
}

func (e *geminiEncoder) finish() {
	res := e.col.finish()
	e.flushTools()
	finish := geminiFinish(res.Stop)
	if e.col.err != "" {
		// an upstream error is not a normal stop
		finish = "OTHER"
	}
	e.emit(geminiBody(e.responseID(), e.model, nil, finish, geminiUsage(res.Usage)))
}

// ---- errors -------------------------------------------------------------------

// geminiStatus is the gRPC-style status name Gemini uses for an HTTP code.
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
	if code >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}

// writeGeminiError answers in the Gemini error shape.
func writeGeminiError(w http.ResponseWriter, status int, msg string) int {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}})
	return status
}
