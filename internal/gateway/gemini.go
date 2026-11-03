package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// ---- Gemini API ---------------------------------------------------------------
//
// Gemini CLI and other Gemini API clients talk to the gateway as if it were
// Google's generativelanguage endpoint. No upstream speaks Gemini, so it is
// a client protocol only: a request is parsed into the IR and the reply is
// rendered from it, exactly as for the other client APIs.

// clientGemini is the Gemini API as a client protocol. It is deliberately
// not in provider.Protocols: no vendor speaks it.
const clientGemini provider.Protocol = "gemini"

// geminiMethods are the methods every catalog model accepts.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFileData struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gFuncCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type gFuncResp struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

type gPart struct {
	Text             *string    `json:"text,omitempty"`
	Thought          bool       `json:"thought,omitempty"`
	ThoughtSignature string     `json:"thoughtSignature,omitempty"`
	InlineData       *gBlob     `json:"inlineData,omitempty"`
	FileData         *gFileData `json:"fileData,omitempty"`
	FunctionCall     *gFuncCall `json:"functionCall,omitempty"`
	FunctionResponse *gFuncResp `json:"functionResponse,omitempty"`
}

type gContent struct {
	Role  string  `json:"role"`
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

// parseGemini reads a generateContent request. Streaming is not in the body;
// the caller says whether the URL asked for it.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	var sys []string
	if g.SystemInstruction != nil {
		sys = append(sys, geminiText(g.SystemInstruction.Parts))
	}

	// Calls without an id get one here, so results can be matched to them:
	// a result without an id belongs to the latest call of the same name.
	lastCall := map[string]string{}
	for _, c := range g.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		msg := Message{Role: role}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + newID()
				}
				lastCall[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiArgsIn(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = lastCall[fr.Name]
				}
				text, failed := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: failed})
			case p.Thought:
				if p.Text != nil && *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: *p.Text})
				}
			case p.Text != nil:
				if *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
				}
			case p.InlineData != nil:
				if strings.HasPrefix(p.InlineData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, attachmentNote(p.InlineData.MimeType))
				}
			case p.FileData != nil:
				msg.Parts = append(msg.Parts, attachmentNote(p.FileData.MimeType))
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
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: lowerSchema(schema)})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO":
			r.ToolChoice = "auto"
		case "ANY":
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		}
		if len(fc.AllowedFunctionNames) > 0 {
			r.Tools = filterTools(r.Tools, fc.AllowedFunctionNames)
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp = gc.Temperature
		r.TopP = gc.TopP
		r.Stop = gc.StopSequences
		var schema json.RawMessage
		if len(gc.ResponseJSONSchema) > 0 {
			schema = gc.ResponseJSONSchema
		} else if len(gc.ResponseSchema) > 0 {
			schema = lowerSchema(gc.ResponseSchema)
		}
		if gc.ResponseMimeType == "application/json" || len(schema) > 0 {
			sys = append(sys, jsonInstruction(schema))
		}
		if tc := gc.ThinkingConfig; tc != nil {
			if tc.ThinkingBudget != nil {
				switch n := *tc.ThinkingBudget; {
				case n == -1: // the model decides: a middling effort
					r.Effort = "medium"
				case n > 0:
					r.Effort = effortOfBudget(n)
				}
				if r.Effort != "" {
					r.Thinking = true
				}
			}
			if tc.ThinkingLevel != "" {
				if e := effortOf(tc.ThinkingLevel); e != "" {
					r.Effort = e
					r.Thinking = true
				}
			}
			if tc.IncludeThoughts {
				r.Thinking = true
			}
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// geminiText joins the text parts of a Gemini content.
func geminiText(parts []gPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text != nil && !p.Thought {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}

// geminiArgsIn reads a call's args; absent or null means no arguments.
func geminiArgsIn(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	return json.RawMessage(s)
}

// geminiResult reads a function response: the text a tool returned, and
// whether it reported an error.
func geminiResult(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return s, false
	}
	_, failed := m["error"]
	for _, k := range []string{"output", "result", "content"} {
		if v, ok := m[k].(string); ok {
			return v, failed
		}
	}
	return s, failed
}

// attachmentNote stands in for an attachment the vendors cannot take.
func attachmentNote(mime string) Part {
	if mime == "" {
		mime = "unknown type"
	}
	return Part{Kind: Text, Text: "[attachment omitted: " + mime + " is not supported by this model]"}
}

// filterTools keeps the tools the client allowed to be called.
func filterTools(tools []Tool, names []string) []Tool {
	var out []Tool
	for _, t := range tools {
		for _, n := range names {
			if t.Name == n {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// jsonInstruction asks for JSON output in words, for vendors with no such
// switch. The schema, when there is one, goes along as text.
func jsonInstruction(schema json.RawMessage) string {
	s := "Respond only with a single valid JSON value. Output no prose, no markdown and no code fences."
	if len(schema) > 0 {
		s += " The JSON must conform to this JSON Schema:\n" + string(schema)
	}
	return s
}

// lowerSchema turns Gemini's upper-case type names (OBJECT, STRING…) into
// standard lower-case JSON Schema types, all the way down.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
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

// callArgs checks that a call's arguments are a complete JSON object. An
// empty string means no arguments. Anything else is incomplete, and such a
// call is never sent.
func callArgs(s string) (json.RawMessage, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage("{}"), true
	}
	if strings.HasPrefix(s, "{") && json.Valid([]byte(s)) {
		return json.RawMessage(s), true
	}
	return nil, false
}

// ---- replies --------------------------------------------------------------------

func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage maps the IR usage to Gemini's counts. The prompt count includes
// cache hits; reasoning tokens are counted apart from the candidates.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	out := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand,
		"totalTokenCount": prompt + cand + u.Reasoning}
	if u.CacheRead > 0 {
		out["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		out["thoughtsTokenCount"] = u.Reasoning
	}
	return out
}

// renderGemini is the non-streaming reply.
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
			args, ok := callArgs(argsString(p))
			if !ok {
				continue
			}
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": args}})
		}
	}
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "resp-" + newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": parts},
			"finishReason": stopToGemini(res.Stop), "index": 0}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
	return b
}

// geminiEncoder writes a Gemini stream: one GenerateContentResponse per
// event. A function call is held until its arguments are complete, then sent
// whole, so no client ever sees a half-written call.
type geminiEncoder struct {
	w      *sseWriter
	model  string
	id     string
	usage  Usage
	stop   string
	failed bool

	tool     bool // a function call is open
	toolID   string
	toolName string
	args     strings.Builder
}

func (e *geminiEncoder) responseID() string {
	if e.id == "" {
		e.id = "resp-" + newID()
	}
	return e.id
}

func (e *geminiEncoder) send(parts []map[string]any) {
	e.w.event("", map[string]any{
		"candidates":   []map[string]any{{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}},
		"modelVersion": e.model,
		"responseId":   e.responseID(),
	})
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
		e.usage.add(ev.Usage)
	case KText:
		e.closeTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text}})
		}
	case KThink:
		e.closeTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text, "thought": true}})
		}
	case KToolStart:
		e.closeTool()
		e.tool, e.toolID, e.toolName = true, ev.ID, ev.Name
		e.args.Reset()
	case KToolArgs:
		if e.tool {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.closeTool()
		e.stop = ev.Stop
	case KUsage:
		e.usage.add(ev.Usage)
	case KError:
		e.tool = false
		if !e.failed {
			e.failed = true
			e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text,
				"status": geminiStatus(502)}})
		}
	}
}

// closeTool sends the open function call, if its arguments are complete.
func (e *geminiEncoder) closeTool() {
	if !e.tool {
		return
	}
	e.tool = false
	args, ok := callArgs(e.args.String())
	e.args.Reset()
	if !ok {
		return
	}
	id := e.toolID
	if id == "" {
		id = "call_" + newID()
	}
	e.send([]map[string]any{{"functionCall": map[string]any{"id": id, "name": e.toolName, "args": args}}})
}

func (e *geminiEncoder) finish() {
	e.closeTool()
	if e.failed {
		return
	}
	e.w.event("", map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": []any{}},
			"finishReason": stopToGemini(e.stop), "index": 0}},
		"usageMetadata": geminiUsage(e.usage),
		"modelVersion":  e.model,
		"responseId":    e.responseID(),
	})
}

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
	case 502, 503, 529:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	if code >= 500 {
		return "INTERNAL"
	}
	return "INVALID_ARGUMENT"
}

// ---- routes -----------------------------------------------------------------------

// geminiModels lists every catalog model in the Gemini models shape.
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
			"description":                "Served by dial via " + e.Provider.Name,
			"supportedGenerationMethods": geminiMethods,
		})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiModel routes /v1beta/models/{model}:{method}. The model id may hold
// slashes and colons, so the method is whatever follows the last colon.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i <= 0 {
		writeError(w, clientGemini, http.StatusNotFound, "dial serves "+apiHint)
		return
	}
	model, method := rest[:i], rest[i+1:]
	switch method {
	case "generateContent":
		s.geminiCall(w, r, model, false)
	case "streamGenerateContent":
		s.geminiCall(w, r, model, true)
	case "countTokens":
		s.geminiCount(w, r, model)
	default:
		writeError(w, clientGemini, http.StatusNotFound,
			fmt.Sprintf("dial has no method %q; it serves %s", method, strings.Join(geminiMethods, ", ")))
	}
}

// geminiCall is the request path of generateContent and its streaming form.
func (s *Server) geminiCall(w http.ResponseWriter, r *http.Request, model string, stream bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, clientGemini, 400, err.Error())
		return
	}
	s.serve(w, r, clientGemini, model, body, stream)
}

// geminiCount answers countTokens with a rough estimate. The request may be
// given directly or wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, model string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, clientGemini, 400, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, clientGemini, 404, unknownModelMsg(model))
		return
	}
	inner := body
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 {
		inner = wrap.GenerateContentRequest
	}
	req, err := parseGemini(inner)
	if err != nil {
		writeError(w, clientGemini, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"totalTokens": estimateTokens(req)})
}

// estimateTokens is about a quarter of the text a request carries.
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
