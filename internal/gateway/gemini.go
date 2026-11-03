package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// The Gemini API is served to agents such as Gemini CLI. Its requests are
// decoded into the same Request the other APIs decode into, and replies are
// encoded from the same Event and Result, so the translation in between is
// shared. No vendor speaks Gemini; it only faces the client.

// geminiMethods are the model methods the gateway serves.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// splitGemini takes the "{model}:{method}" tail of a Gemini model path. The
// method follows the last colon, because model ids may contain colons.
func splitGemini(rest string) (model, method string, ok bool) {
	i := strings.LastIndex(rest, ":")
	if i <= 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// ---- wire types -------------------------------------------------------------

type gReq struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Tools             []gTool    `json:"tools"`
	ToolConfig        *gToolCfg  `json:"toolConfig"`
	GenerationConfig  *gGen      `json:"generationConfig"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gPart struct {
	Text             string     `json:"text"`
	Thought          bool       `json:"thought"`
	ThoughtSignature string     `json:"thoughtSignature"`
	InlineData       *gBlob     `json:"inlineData"`
	FileData         *gFile     `json:"fileData"`
	FunctionCall     *gFuncCall `json:"functionCall"`
	FunctionResponse *gFuncResp `json:"functionResponse"`
}

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFile struct {
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

type gTool struct {
	FunctionDeclarations []gFuncDecl `json:"functionDeclarations"`
}

type gFuncDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
}

type gToolCfg struct {
	FunctionCallingConfig *struct {
		Mode                 string   `json:"mode"`
		AllowedFunctionNames []string `json:"allowedFunctionNames"`
	} `json:"functionCallingConfig"`
}

type gGen struct {
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

// ---- requests ---------------------------------------------------------------

// parseGemini decodes a generateContent request. Streaming is not in the
// body; the caller sets Stream from the method.
func parseGemini(body []byte) (*Request, error) {
	var g gReq
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	if g.SystemInstruction != nil {
		r.System = geminiText(g.SystemInstruction.Parts)
	}
	// Function responses may leave out the call id; they are matched to the
	// latest call of the same function name.
	latest := map[string]string{}
	calls := 0
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				f := p.FunctionCall
				id := f.ID
				if id == "" {
					calls++
					id = fmt.Sprintf("gcall_%d", calls)
				}
				latest[f.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: f.Name, Args: parseArgs(string(f.Args))})
			case p.FunctionResponse != nil:
				f := p.FunctionResponse
				id := f.ID
				if id == "" {
					id = latest[f.Name]
				}
				if id == "" {
					// Upstreams reject a result that answers no call, and
					// the function name is not a call id, so refuse it.
					return nil, fmt.Errorf("functionResponse %q has no functionCall to answer", f.Name)
				}
				text, isErr := geminiResponseText(f.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			case p.InlineData != nil && strings.HasPrefix(p.InlineData.MimeType, "image/"):
				msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
			case p.InlineData != nil:
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment " + p.InlineData.MimeType + " omitted]"})
			case p.FileData != nil:
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[file " + p.FileData.FileURI + " omitted]"})
			case p.Thought:
				msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}
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
		switch fc.Mode {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			r.ToolChoice = "required"
			// An allowed list restricts the calls to those functions. The
			// tools off the list are not offered, so this holds for every
			// upstream. A list naming no declared function cannot be met,
			// and offering every tool instead would break the restriction.
			if allowed := fc.AllowedFunctionNames; len(allowed) > 0 {
				var kept []Tool
				for _, t := range r.Tools {
					if containsString(allowed, t.Name) {
						kept = append(kept, t)
					}
				}
				if len(kept) == 0 {
					return nil, fmt.Errorf("allowedFunctionNames %s name no declared function", strings.Join(allowed, ", "))
				}
				r.Tools = kept
				if len(allowed) == 1 {
					r.ToolChoice = "name:" + allowed[0]
				}
			}
		case "AUTO":
			r.ToolChoice = "auto"
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		// The shared request has no JSON output mode, so a JSON reply is
		// asked for in the system instruction, with the schema.
		if gc.ResponseMimeType == "application/json" || len(gc.ResponseSchema) > 0 || len(gc.ResponseJSONSchema) > 0 {
			r.System = appendJSONInstruction(r.System, gc.ResponseSchema, gc.ResponseJSONSchema)
		}
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			r.Thinking = tc.IncludeThoughts
			switch {
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget < 0:
				r.Effort = "medium" // -1: the model decides
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget > 0:
				r.Effort = effortOfBudget(*tc.ThinkingBudget)
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			}
		}
	}
	return r, nil
}

// geminiText joins the text of parts that are not reasoning.
func geminiText(parts []gPart) string {
	var b strings.Builder
	for _, p := range parts {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// containsString reports whether s is in list.
func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// lowerSchema turns a Gemini schema, whose type names are upper case
// (OBJECT, STRING, ...), into a standard lower-case JSON Schema. Schemas that
// are already standard pass through unchanged. A schema that is not JSON is
// returned as it is.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return raw
	}
	out, err := json.Marshal(lowerSchemaNode(v))
	if err != nil {
		return raw
	}
	return out
}

// lowerSchemaNode lowers the type names of one schema and of the schemas
// inside it. Property names are data, not keywords, so a property called
// "type" is left alone.
func lowerSchemaNode(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for k, val := range m {
		switch k {
		case "type":
			m[k] = lowerTypeName(val)
		case "properties", "$defs", "definitions", "patternProperties":
			if sub, ok := val.(map[string]any); ok {
				for name, s := range sub {
					sub[name] = lowerSchemaNode(s)
				}
			}
		case "items", "additionalProperties", "not", "if", "then", "else", "contains", "propertyNames":
			m[k] = lowerSchemaNode(val)
		case "anyOf", "oneOf", "allOf", "prefixItems":
			if arr, ok := val.([]any); ok {
				for i := range arr {
					arr[i] = lowerSchemaNode(arr[i])
				}
			}
		}
	}
	return m
}

// lowerTypeName lowers a type name, or each name of a list of them.
func lowerTypeName(v any) any {
	switch t := v.(type) {
	case string:
		return strings.ToLower(t)
	case []any:
		for i := range t {
			if s, ok := t[i].(string); ok {
				t[i] = strings.ToLower(s)
			}
		}
	}
	return v
}

// appendJSONInstruction asks for a reply of JSON only, matching the schema
// when there is one. The schema is given in standard form.
func appendJSONInstruction(system string, schemas ...json.RawMessage) string {
	msg := "Reply with a single JSON value only: no prose, no markdown, no code fences."
	for _, s := range schemas {
		if len(s) > 0 {
			msg += " The JSON must match this JSON schema: " + string(lowerSchema(s))
			break
		}
	}
	if system == "" {
		return msg
	}
	return system + "\n\n" + msg
}

// geminiResponseText reads a function response: the "output" or "error"
// string Gemini clients put there, or else the JSON as it is.
func geminiResponseText(raw json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		for _, key := range []string{"output", "error"} {
			v, ok := m[key]
			if !ok {
				continue
			}
			var s string
			if json.Unmarshal(v, &s) != nil {
				s = string(v)
			}
			return s, key == "error"
		}
	}
	return string(raw), false
}

// ---- replies ----------------------------------------------------------------

// renderGemini is a whole reply in the Gemini API's shape. thoughts says
// whether the client asked for reasoning to be returned.
func renderGemini(res Result, model string, thoughts bool) []byte {
	parts, reason := res.Parts, geminiFinish(res.Stop)
	if res.Cut {
		// The reply has no stop, so an open call's arguments are incomplete
		// and it is not sent.
		if n := len(parts); n > 0 && parts[n-1].Kind == ToolCall {
			parts = parts[:n-1]
		}
		reason = "OTHER"
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": geminiParts(parts, thoughts)},
		"finishReason": reason, "index": 0}
	v := map[string]any{"candidates": []any{cand}, "usageMetadata": geminiUsage(res.Usage), "modelVersion": model}
	if res.ID != "" {
		v["responseId"] = res.ID
	}
	out, _ := json.Marshal(v)
	return out
}

// geminiParts lists the parts of a reply. Reasoning is listed only when
// thoughts is set, as the Gemini API does without includeThoughts.
func geminiParts(parts []Part, thoughts bool) []any {
	out := []any{}
	for _, p := range parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text})
			}
		case Thinking:
			if !thoughts {
				continue
			}
			if p.Text != "" || p.Signature != "" {
				m := map[string]any{"text": p.Text, "thought": true}
				if p.Signature != "" {
					m["thoughtSignature"] = p.Signature
				}
				out = append(out, m)
			}
		case ToolCall:
			fc := map[string]any{"name": p.Name, "args": parseArgs(argsString(p))}
			if p.ID != "" {
				fc["id"] = p.ID
			}
			out = append(out, map[string]any{"functionCall": fc})
		}
	}
	return out
}

// geminiFinish maps a stop reason to a Gemini finishReason. A call to a
// tool is a normal stop for Gemini.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage counts tokens the Gemini way: the prompt includes cache hits,
// and reasoning tokens are counted apart from the candidate tokens.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead + u.CacheWrite
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

// geminiEncoder streams a reply as Gemini API chunks. A function call is
// sent whole, once its arguments have ended. The last chunk carries the
// finish reason and the usage.
type geminiEncoder struct {
	w        *sseWriter
	model    string
	thoughts bool // the client asked for reasoning to be returned
	id       string
	usage    Usage
	stop     string // the upstream's stop; empty until it arrives

	tool     bool // a function call is open
	toolID   string
	toolName string
	args     strings.Builder

	sig strings.Builder // thinking signature, sent once its thinking ends
}

func (g *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			g.id = ev.MsgID
		}
		g.usage.add(ev.Usage)
		return
	case KUsage:
		g.usage.add(ev.Usage)
		return
	case KToolArgs:
		if g.tool {
			g.args.WriteString(ev.Text)
		}
		return
	case KSig:
		g.sig.WriteString(ev.Text)
		return
	}
	// Any other event ends the thinking the signature belongs to.
	g.flushSig()
	switch ev.Kind {
	case KText:
		g.flushTool()
		if ev.Text != "" {
			g.chunk([]any{map[string]any{"text": ev.Text}})
		}
	case KThink:
		g.flushTool()
		if ev.Text != "" && g.thoughts {
			g.chunk([]any{map[string]any{"text": ev.Text, "thought": true}})
		}
	case KToolStart:
		g.flushTool()
		g.tool, g.toolID, g.toolName = true, ev.ID, ev.Name
		g.args.Reset()
	case KStop:
		g.flushTool()
		if ev.Stop != "" {
			g.stop = ev.Stop
		}
	case KError:
		// The stream broke off. Without a stop the open call is dropped
		// by finish, which also sets the finish reason.
	}
}

// flushSig sends the thinking signature, if any, as a part of its own.
func (g *geminiEncoder) flushSig() {
	if g.sig.Len() == 0 {
		return
	}
	if g.thoughts {
		g.chunk([]any{map[string]any{"text": "", "thought": true, "thoughtSignature": g.sig.String()}})
	}
	g.sig.Reset()
}

// flushTool sends the open function call, if any, with complete arguments.
func (g *geminiEncoder) flushTool() {
	if !g.tool {
		return
	}
	g.tool = false
	fc := map[string]any{"name": g.toolName, "args": parseArgs(g.args.String())}
	if g.toolID != "" {
		fc["id"] = g.toolID
	}
	g.chunk([]any{map[string]any{"functionCall": fc}})
}

func (g *geminiEncoder) finish() {
	g.flushSig()
	reason := geminiFinish(g.stop)
	if g.stop == "" {
		// The upstream ended, or broke off, without a stop: the open call's
		// arguments may be incomplete, so the call is not sent.
		g.tool = false
		reason = "OTHER"
	}
	g.flushTool()
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": []any{}},
		"finishReason": reason, "index": 0}
	g.send(cand, geminiUsage(g.usage))
}

func (g *geminiEncoder) chunk(parts []any) {
	g.send(map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}, nil)
}

func (g *geminiEncoder) send(cand map[string]any, usage map[string]any) {
	v := map[string]any{"candidates": []any{cand}, "modelVersion": g.model}
	if g.id != "" {
		v["responseId"] = g.id
	}
	if usage != nil {
		v["usageMetadata"] = usage
	}
	g.w.event("", v)
}

// ---- HTTP -------------------------------------------------------------------

// gemini serves the model methods under /v1beta/models/{model}:{method}.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	model, method, ok := splitGemini(r.PathValue("rest"))
	if !ok {
		writeError(w, provider.Gemini, 404, "dial serves the Gemini methods "+strings.Join(geminiMethods, ", ")+" under /v1beta/models")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	stream := false
	switch method {
	case "generateContent":
	case "streamGenerateContent":
		if r.URL.Query().Get("alt") != "sse" {
			writeError(w, provider.Gemini, 400, "dial streams Gemini replies only as server-sent events; add alt=sse")
			return
		}
		stream = true
	case "countTokens":
		s.geminiCount(w, model, body)
		return
	default:
		writeError(w, provider.Gemini, 404, "dial has no Gemini method "+method)
		return
	}
	s.serve(w, r, provider.Gemini, model, body, &stream)
}

// geminiModels lists the whole catalog as Gemini models.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		name := e.Name
		if name == "" {
			name = e.ID
		}
		models = append(models, map[string]any{"name": "models/" + e.ID, "version": Version,
			"displayName": name, "supportedGenerationMethods": geminiMethods})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiCount estimates the tokens of a request, given either directly or
// wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, provider.Gemini, 404, unknownModelMessage(model))
		return
	}
	var wrap struct {
		Wrapped json.RawMessage `json:"generateContentRequest"`
	}
	json.Unmarshal(body, &wrap)
	if len(wrap.Wrapped) > 0 {
		body = wrap.Wrapped
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"totalTokens": roughTokens(req)})
}

// geminiStatus is the gRPC-style status name Gemini errors carry.
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
	case 502, 503, 529:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	return "UNKNOWN"
}
