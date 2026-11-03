package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// The Gemini API is a client protocol only. Gemini clients call dial, dial
// parses each call into the shared Request, and the reply is rendered back
// in Gemini's shape. No provider is ever reached with this protocol.

// geminiHint names the Gemini routes, for 404 replies.
const geminiHint = "dial serves the Gemini API at /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens, and lists models at /v1beta/models"

// ---- request ----------------------------------------------------------------

type gPart struct {
	Text             *string `json:"text"`
	Thought          bool    `json:"thought"`
	ThoughtSignature string  `json:"thoughtSignature"`
	InlineData       *gBlob  `json:"inlineData"`
	FileData         *gFile  `json:"fileData"`
	FunctionCall     *gCall  `json:"functionCall"`
	FunctionResponse *gReply `json:"functionResponse"`
}

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFile struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gReply struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
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

type gRequest struct {
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

// parseGemini reads a generateContent body. The model and stream flag come
// from the URL, so the caller sets them.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	if g.SystemInstruction != nil {
		r.System = geminiText(g.SystemInstruction.Parts)
	}
	calls := map[string]string{} // function name → id of its latest call
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
		// tool results sent in a model turn belong to a user turn after it
		var results []Part
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = newCallID()
				}
				calls[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: argsFromRaw(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = calls[fr.Name]
				}
				if id == "" {
					id = newCallID()
				}
				out, failed := responseOutput(fr.Response)
				part := Part{Kind: ToolResult, CallID: id, Text: out, IsError: failed}
				if c.Role == "model" {
					results = append(results, part)
				} else {
					msg.Parts = append(msg.Parts, part)
				}
			case p.Thought && p.Text != nil:
				if *p.Text != "" || p.ThoughtSignature != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: *p.Text, Signature: p.ThoughtSignature})
				}
			case p.Text != nil:
				if *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
				}
			case p.InlineData != nil:
				d := p.InlineData
				if strings.HasPrefix(d.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: d.MimeType, Data: d.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment " + d.MimeType + " omitted]"})
				}
			case p.FileData != nil:
				f := p.FileData
				if strings.HasPrefix(f.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: f.MimeType, URL: f.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[file " + f.FileURI + "]"})
				}
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
		if len(results) > 0 {
			r.Messages = append(r.Messages, Message{Role: "user", Parts: results})
		}
	}
	for _, t := range g.Tools {
		for _, f := range t.FunctionDeclarations {
			schema := f.Parameters
			if len(schema) == 0 {
				schema = f.ParametersJSONSchema
			}
			if len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			} else {
				schema = lowerSchema(schema)
			}
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: schema})
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
			// ANY with a single allowed function means that function; with no
			// allow-list every declared function is allowed
			allowed := fc.AllowedFunctionNames
			if len(allowed) == 0 {
				for _, t := range r.Tools {
					allowed = append(allowed, t.Name)
				}
			}
			if len(allowed) == 1 {
				r.ToolChoice = "name:" + allowed[0]
			} else {
				r.ToolChoice = "required"
			}
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			geminiThinking(r, tc.ThinkingBudget, tc.ThinkingLevel, tc.IncludeThoughts)
		}
		if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") {
			// no upstream field carries this everywhere, so ask in the system prompt
			schema := gc.ResponseSchema
			if len(schema) == 0 {
				schema = gc.ResponseJSONSchema
			}
			r.System = joinText(r.System, jsonInstruction(schema))
		}
	}
	return r, nil
}

// geminiThinking maps thinkingConfig onto the shared effort levels. A budget
// of 0 turns thinking off; -1 lets the model decide, which counts as medium.
func geminiThinking(r *Request, budget *int, level string, include bool) {
	switch {
	case budget != nil:
		n := *budget
		if n == 0 {
			return
		}
		r.Thinking = true
		if n < 0 {
			r.Effort = "medium"
		} else {
			r.Effort = effortOfBudget(n)
		}
	case level != "":
		r.Thinking, r.Effort = true, effortOf(level)
	case include:
		r.Thinking = true
	}
}

// jsonInstruction asks for JSON in the system prompt, with the schema if any.
func jsonInstruction(schema json.RawMessage) string {
	const head = "Respond only with a JSON value: no prose and no code fences, nothing but the JSON."
	if len(schema) == 0 {
		return head
	}
	return head + " The JSON must conform to this JSON schema:\n" + string(lowerSchema(schema))
}

// geminiText joins the visible text parts of a Gemini content.
func geminiText(parts []gPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text != nil && !p.Thought {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}

// joinText joins two system texts, skipping an empty one.
func joinText(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

// argsFromRaw is a function call's arguments as a JSON object, never empty.
func argsFromRaw(raw json.RawMessage) json.RawMessage {
	if s := strings.TrimSpace(string(raw)); s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return parseArgs(string(raw))
}

// responseOutput is the text a function response carries, and whether it is
// an error. A response of {"output": "..."} is its string; anything else goes
// through as its JSON.
func responseOutput(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if e, ok := m["error"]; ok {
			if str, ok := e.(string); ok {
				return str, true
			}
			b, _ := json.Marshal(e)
			return string(b), true
		}
		if o, ok := m["output"].(string); ok && len(m) == 1 {
			return o, false
		}
	}
	return s, false
}

// geminiTypes maps Gemini's upper-case schema types to JSON Schema's.
var geminiTypes = map[string]string{"OBJECT": "object", "STRING": "string", "INTEGER": "integer",
	"NUMBER": "number", "BOOLEAN": "boolean", "ARRAY": "array", "NULL": "null"}

// lowerSchema converts the types of a Gemini schema to lower case. Anything
// that does not parse is returned as it was.
func lowerSchema(raw json.RawMessage) json.RawMessage {
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
		for k, e := range x {
			if k == "type" {
				x[k] = lowerType(e)
			} else {
				x[k] = lowerTypes(e)
			}
		}
	case []any:
		for i := range x {
			x[i] = lowerTypes(x[i])
		}
	}
	return v
}

// lowerType handles the value of a "type" key. A property that happens to be
// called "type" holds a schema, not a type name, so maps are walked instead.
func lowerType(v any) any {
	switch t := v.(type) {
	case string:
		if l, ok := geminiTypes[t]; ok {
			return l
		}
		return t
	case []any:
		for i := range t {
			t[i] = lowerType(t[i])
		}
		return t
	}
	return lowerTypes(v)
}

// ---- streamed reply -----------------------------------------------------------

// geminiEncoder writes events as a Gemini stream: one event per part, in the
// order the reply produced them, then a final event carrying the finish
// reason and usage. A function call is sent once its arguments are complete.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	usage Usage
	stop  string

	open     bool // a function call is still receiving its arguments
	callID   string
	callName string
	args     strings.Builder
}

func (e *geminiEncoder) send(part map[string]any) {
	e.w.event("", map[string]any{"candidates": []map[string]any{{
		"content": map[string]any{"role": "model", "parts": []map[string]any{part}},
		"index":   0,
	}}})
}

func (e *geminiEncoder) closeCall() {
	if !e.open {
		return
	}
	e.open = false
	e.send(map[string]any{"functionCall": map[string]any{"id": e.callID, "name": e.callName, "args": parseArgs(e.args.String())}})
	e.args.Reset()
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		e.usage.add(ev.Usage)
		if e.id == "" {
			e.id = ev.MsgID
		}
	case KText:
		e.closeCall()
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.closeCall()
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text, "thought": true})
		}
	case KToolStart:
		e.closeCall()
		e.open = true
		e.callID, e.callName = ev.ID, ev.Name
		if e.callID == "" {
			e.callID = "call_" + newID()
		}
	case KToolArgs:
		if e.open {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.closeCall()
		e.stop = ev.Stop
	case KUsage:
		e.usage.add(ev.Usage)
	case KError:
		e.closeCall()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
}

func (e *geminiEncoder) finish() {
	e.closeCall()
	id := e.id
	if id == "" {
		id = newID()
	}
	e.w.event("", map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": []any{}},
			"finishReason": geminiFinish(e.stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(e.usage),
		"modelVersion":  e.model,
		"responseId":    id,
	})
}

// renderGemini is the whole reply, for non-streaming clients.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text != "" || p.Signature != "" {
				part := map[string]any{"text": p.Text, "thought": true}
				if p.Signature != "" {
					part["thoughtSignature"] = p.Signature
				}
				parts = append(parts, part)
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

// geminiFinish maps a stop reason onto Gemini's. Tool calls end with STOP.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage reports the prompt side as the total the provider counted, and
// the thinking tokens apart.
func geminiUsage(u Usage) map[string]any {
	in := u.Input + u.CacheRead
	m := map[string]any{"promptTokenCount": in, "candidatesTokenCount": u.Output,
		"totalTokenCount": in + u.Output, "thoughtsTokenCount": u.Reasoning}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

// ---- HTTP -----------------------------------------------------------------------

// geminiRoute serves /v1beta/models/{rest}, where rest is "<model>:<method>".
// The model is a catalog reference, so it may contain "/", and a tag may add
// ":"; the method is whatever follows the last colon.
func (s *Server) geminiRoute(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, provider.Gemini, 404, geminiHint)
		return
	}
	model, method := rest[:i], rest[i+1:]
	switch method {
	case "generateContent", "streamGenerateContent":
		body, err := readBody(r)
		if err != nil {
			writeError(w, provider.Gemini, 400, err.Error())
			return
		}
		stream := method == "streamGenerateContent"
		s.serve(w, r, provider.Gemini, body, model, func() (*Request, error) {
			req, err := parseGemini(body)
			if err != nil {
				return nil, err
			}
			req.Model, req.Stream = model, stream
			return req, nil
		})
	case "countTokens":
		s.geminiCount(w, r, model)
	default:
		writeError(w, provider.Gemini, 404, "dial has no Gemini method "+method+"; "+geminiHint)
	}
}

// geminiCount estimates the tokens of a request. It accepts the request
// itself or one wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, model string) {
	body, err := readBody(r)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
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
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	writeGemini(w, http.StatusOK, map[string]any{"totalTokens": estimateTokens(req)})
}

// estimateTokens is a rough count, about four characters to a token, in the
// same spirit as the Anthropic count_tokens fallback.
func estimateTokens(r *Request) int {
	n := len(r.System)
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range r.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return n / 4
}

// geminiModels lists the catalog in Gemini's shape.
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
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		})
	}
	writeGemini(w, http.StatusOK, map[string]any{"models": models})
}

// writeGemini answers with compact JSON, as the Gemini API does.
func writeGemini(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	w.Write(b)
}

// geminiError writes an error in Gemini's shape, with its status name.
func geminiError(w http.ResponseWriter, status int, msg string) {
	writeGemini(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}})
}

// geminiStatus names an HTTP status the way Gemini does.
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
	case 502, 503, 529:
		return "UNAVAILABLE"
	}
	if code >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}
