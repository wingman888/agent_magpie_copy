package gateway

// The Gemini API (generativelanguage.googleapis.com) is served here to
// clients only. A Gemini request is parsed into the same Request the other
// three APIs parse into, the chosen provider is reached over Chat,
// Responses or Anthropic as usual, and the reply is rendered back in Gemini's
// shape. Nothing in this file is used when an OpenAI or Anthropic client is
// served.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// ---- Gemini request ---------------------------------------------------------

type gPart struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	ThoughtSignature string `json:"thoughtSignature"`
	InlineData       *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData"`
	FileData *struct {
		MimeType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData"`
	FunctionCall *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gRequest struct {
	Contents          []gContent      `json:"contents"`
	SystemInstruction json.RawMessage `json:"systemInstruction"`
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

// parseGemini reads a generateContent body. The model comes from the URL,
// so it is not set here.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{System: geminiSystem(g.SystemInstruction)}

	// A functionResponse without an id answers the latest call of the same name.
	ids := map[string]string{}
	for _, c := range g.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		var parts []Part
		for _, gp := range c.Parts {
			switch {
			case gp.FunctionCall != nil:
				fc := gp.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + newID()
				}
				ids[fc.Name] = id
				parts = append(parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: parseArgs(string(fc.Args))})
			case gp.FunctionResponse != nil:
				fr := gp.FunctionResponse
				id := fr.ID
				if id == "" {
					id = ids[fr.Name]
				}
				if id == "" {
					id = "call_" + newID()
				}
				text, isErr := geminiResult(fr.Response)
				parts = append(parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			case gp.InlineData != nil:
				d := gp.InlineData
				if strings.HasPrefix(d.MimeType, "image/") {
					parts = append(parts, Part{Kind: Image, MediaType: d.MimeType, Data: d.Data})
				} else {
					parts = append(parts, Part{Kind: Text, Text: "[attachment: " + d.MimeType + "]"})
				}
			case gp.FileData != nil:
				d := gp.FileData
				if strings.HasPrefix(d.MimeType, "image/") {
					parts = append(parts, Part{Kind: Image, MediaType: d.MimeType, URL: d.FileURI})
				} else {
					parts = append(parts, Part{Kind: Text, Text: "[attachment: " + d.MimeType + "]"})
				}
			case gp.Thought:
				if gp.Text != "" {
					parts = append(parts, Part{Kind: Thinking, Text: gp.Text, Signature: gp.ThoughtSignature})
				}
			case gp.Text != "":
				parts = append(parts, Part{Kind: Text, Text: gp.Text})
			}
		}
		if len(parts) > 0 {
			r.Messages = append(r.Messages, Message{Role: role, Parts: parts})
		}
	}

	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.Parameters
			if len(schema) == 0 {
				schema = fd.ParametersJSONSchema
			}
			if schema = geminiSchema(schema); len(schema) == 0 {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: schema})
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
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			budget := 0
			hasBudget := tc.ThinkingBudget != nil
			if hasBudget {
				budget = *tc.ThinkingBudget
			}
			switch {
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			case hasBudget && budget < 0: // the model decides; call that medium
				r.Effort = "medium"
			case hasBudget:
				r.Effort = effortOfBudget(budget) // 0 gives "", thinking off
			}
			r.Thinking = tc.IncludeThoughts || r.Effort != ""
			if hasBudget && budget == 0 {
				r.Thinking = false
			}
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(gc.ResponseMimeType)), "application/json") {
			// no upstream has a JSON-mode field everywhere; ask in the system prompt
			schema := geminiSchema(gc.ResponseSchema)
			if len(schema) == 0 {
				schema = geminiSchema(gc.ResponseJSONSchema)
			}
			instr := geminiJSONInstruction(schema)
			if r.System != "" {
				r.System += "\n\n" + instr
			} else {
				r.System = instr
			}
		}
	}
	return r, nil
}

// geminiSystem reads systemInstruction: a Content object, or a bare string.
func geminiSystem(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var c struct {
		Parts []gPart `json:"parts"`
	}
	if json.Unmarshal(raw, &c) == nil {
		var texts []string
		for _, p := range c.Parts {
			if p.Text != "" && !p.Thought {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n\n")
	}
	return stringOrText(raw)
}

// geminiResult is the text of a functionResponse. Gemini clients wrap the
// output as {"output": "..."} or {"error": "..."}; those are unwrapped.
func geminiResult(raw json.RawMessage) (string, bool) {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if s, ok := m["output"].(string); ok {
			return s, false
		}
		if s, ok := m["error"].(string); ok {
			return s, true
		}
	}
	return string(raw), false
}

// geminiJSONInstruction asks for JSON in the system prompt.
func geminiJSONInstruction(schema json.RawMessage) string {
	if len(schema) == 0 {
		return "Respond only with valid JSON, with no other text."
	}
	return "Respond only with JSON that conforms to this JSON schema, with no other text:\n" + string(schema)
}

// geminiSchema turns a Gemini schema (upper-case types such as OBJECT) into
// the lower-case JSON Schema the upstreams expect. Empty input stays empty.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(lowerSchema(v))
	if err != nil {
		return raw
	}
	return out
}

// lowerSchema lower-cases every "type" value, recursively. A property named
// "type" is a schema object, not a type name, so it is left alone.
func lowerSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch {
			case k == "propertyOrdering":
				delete(x, k)
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
				default:
					x[k] = lowerSchema(val)
				}
			default:
				x[k] = lowerSchema(val)
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = lowerSchema(x[i])
		}
		return x
	}
	return v
}

// ---- Gemini reply -----------------------------------------------------------

// stopToGemini maps a dial stop reason to a Gemini finishReason.
func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage counts tokens the way Gemini does. promptTokenCount is the
// upstream prompt count, cache reads included.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	m := map[string]any{
		"promptTokenCount":     prompt,
		"candidatesTokenCount": u.Output,
		"totalTokenCount":      prompt + u.Output,
		"thoughtsTokenCount":   u.Reasoning,
	}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

// geminiArgs is a tool call's arguments as a JSON object.
func geminiArgs(p Part) json.RawMessage { return parseArgs(argsString(p)) }

func geminiCallID(id string) string {
	if id == "" {
		return "call_" + newID()
	}
	return id
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
			parts = append(parts, map[string]any{"functionCall": map[string]any{
				"id": geminiCallID(p.ID), "name": p.Name, "args": geminiArgs(p)}})
		}
	}
	m := res.Model
	if m == "" {
		m = model
	}
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": stopToGemini(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  m,
		"responseId":    id,
	})
	return b
}

// geminiEncoder writes a Gemini SSE stream: one part per event, in order.
// The last part is held back until the stream ends, so that it can carry
// finishReason and usageMetadata itself.
type geminiEncoder struct {
	w      *sseWriter
	model  string
	id     string
	col    collector // stop reason and usage
	held   map[string]any
	tool   bool // a tool call is open
	toolID string
	toolNm string
	args   strings.Builder
	failed bool
}

func (e *geminiEncoder) send(part map[string]any, finish string, usage map[string]any) {
	if e.id == "" {
		e.id = "resp_" + newID()
	}
	parts := []map[string]any{}
	if part != nil {
		parts = append(parts, part)
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	ev := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": e.model, "responseId": e.id}
	if finish != "" {
		cand["finishReason"] = finish
		ev["usageMetadata"] = usage
	}
	e.w.event("", ev)
}

func (e *geminiEncoder) emit(part map[string]any) {
	if e.held != nil {
		e.send(e.held, "", nil)
	}
	e.held = part
}

func (e *geminiEncoder) closeTool() {
	if !e.tool {
		return
	}
	e.tool = false
	e.emit(map[string]any{"functionCall": map[string]any{
		"id": geminiCallID(e.toolID), "name": e.toolNm, "args": parseArgs(e.args.String())}})
	e.args.Reset()
}

func (e *geminiEncoder) event(ev Event) {
	if e.failed {
		return
	}
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
		e.col.add(ev)
	case KText:
		e.closeTool()
		if ev.Text != "" {
			e.emit(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.closeTool()
		if ev.Text != "" {
			e.emit(map[string]any{"text": ev.Text, "thought": true})
		}
	case KToolStart:
		e.closeTool()
		e.tool, e.toolID, e.toolNm = true, ev.ID, ev.Name
		e.args.Reset()
	case KToolArgs:
		if e.tool {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.closeTool()
		e.col.add(ev)
	case KUsage:
		e.col.add(ev)
	case KError:
		e.closeTool()
		if e.held != nil {
			e.send(e.held, "", nil)
			e.held = nil
		}
		e.w.event("", map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
		e.failed = true
	}
}

func (e *geminiEncoder) finish() {
	if e.failed {
		return
	}
	e.closeTool()
	res := e.col.finish()
	e.send(e.held, stopToGemini(res.Stop), geminiUsage(res.Usage))
	e.held = nil
}

// ---- HTTP -------------------------------------------------------------------

// geminiStatus is the Gemini status name for an HTTP status.
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
	case 503, 529:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	if code >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}

// writeGemini answers with compact JSON, as Gemini does.
func writeGemini(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	w.Write(b)
}

// geminiModel describes one catalog entry the way models.list does.
func geminiModel(e provider.Entry) map[string]any {
	return map[string]any{
		"name":                       "models/" + e.ID,
		"version":                    "dial",
		"displayName":                e.Name,
		"description":                "via dial from " + e.Provider.Name,
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
	}
}

// gemini routes the POST calls under /v1beta/models/{model}:{method}. The
// model reference may itself contain slashes and colons; the method is what
// follows the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("rest")
	i := strings.LastIndex(ref, ":")
	if i < 0 {
		writeError(w, provider.Gemini, 404, "dial's Gemini API needs a method: /v1beta/models/{model}:generateContent, :streamGenerateContent or :countTokens")
		return
	}
	model, method := ref[:i], ref[i+1:]
	switch method {
	case "generateContent":
		s.geminiServe(w, r, model, false)
	case "streamGenerateContent":
		s.geminiServe(w, r, model, true)
	case "countTokens":
		s.geminiCount(w, r, model)
	default:
		writeError(w, provider.Gemini, 404, fmt.Sprintf("dial's Gemini API has no method %q", method))
	}
}

// geminiServe answers generateContent and streamGenerateContent.
func (s *Server) geminiServe(w http.ResponseWriter, r *http.Request, ref string, stream bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	start := time.Now()
	call := Call{Time: start, From: provider.Gemini, Model: ref, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(ref)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeError(w, provider.Gemini, 404, unknownModelMsg(ref))
		return
	}
	call.Provider = p.ID
	req, err := parseGemini(body)
	if err == nil && len(req.Messages) == 0 {
		err = fmt.Errorf("contents is required")
	}
	if err != nil {
		call.Status, call.Error = 400, err.Error()
		s.record(call)
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	req.Model, req.Stream = model, stream
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, provider.Gemini, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.translateReq(w, r, p, provider.Gemini, to[0], model, req, &call.Usage)
	s.log(call, start, p, model)
}

// geminiCount answers countTokens with a rough estimate: about four
// characters to a token, and at least one.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, ref string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(ref); !ok {
		writeError(w, provider.Gemini, 404, unknownModelMsg(ref))
		return
	}
	var wrap struct {
		Req json.RawMessage `json:"generateContentRequest"`
	}
	json.Unmarshal(body, &wrap)
	if len(wrap.Req) > 0 {
		body = wrap.Req
	}
	req, err := parseGemini(body)
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
	tokens := (n + 3) / 4
	if tokens < 1 {
		tokens = 1
	}
	writeGemini(w, 200, map[string]any{"totalTokens": tokens})
}

// geminiModels lists the catalog.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModel(e))
	}
	writeGemini(w, 200, map[string]any{"models": models})
}

// geminiModelGet describes one model.
func (s *Server) geminiModelGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("name"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeGemini(w, 200, geminiModel(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, "unknown model "+id)
}
