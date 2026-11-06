package gateway

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

// geminiClient is the Gemini API as a client speaks it. No vendor speaks it,
// so it is only ever a client protocol and never part of provider.Protocols.
const geminiClient provider.Protocol = "gemini"

// ---- routes ---------------------------------------------------------------------

// geminiModels lists the whole catalog in the Gemini API's shape.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, map[string]any{
			"name":                       "models/" + e.ID,
			"version":                    "1",
			"displayName":                e.Name,
			"description":                e.Name + " via dial",
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiPost routes POST /v1beta/models/{model}:{method}. The model id may
// itself contain slashes and colons, so the method is whatever follows the
// last colon.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	i := strings.LastIndex(name, ":")
	if i < 0 {
		writeError(w, geminiClient, 404, "dial serves POST /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	model, method := strings.TrimPrefix(name[:i], "models/"), name[i+1:]
	switch method {
	case "generateContent":
		s.geminiGenerate(w, r, model, false)
	case "streamGenerateContent":
		s.geminiGenerate(w, r, model, true)
	case "countTokens":
		s.geminiCount(w, r, model)
	default:
		writeError(w, geminiClient, 404, fmt.Sprintf("dial has no Gemini method %q", method))
	}
}

// geminiGenerate is the request path of generateContent and
// streamGenerateContent. It mirrors handle, but the model comes from the URL
// and the stream flag from the method.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, model string, stream bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiClient, 400, err.Error())
		return
	}
	start := time.Now()
	call := Call{Time: start, From: geminiClient, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, vendorModel, ok := provider.Resolve(model)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeError(w, geminiClient, 404, unknownModelMsg(model))
		return
	}
	call.Provider = p.ID
	// handle() passes a request straight through when the vendor serves the
	// client's protocol natively. provider.Base has no case for geminiClient,
	// so p.Base(geminiClient) is always "" and no vendor can take the
	// passthrough branch here. Gemini is therefore always translated.
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, geminiClient, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.translate(w, r, p, geminiClient, to[0], vendorModel, geminiBody(body, model, stream), &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: vendorModel,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiCount answers countTokens with a rough estimate. The body is either a
// request or one wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, model string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiClient, 400, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, geminiClient, 404, unknownModelMsg(model))
		return
	}
	var env struct {
		Wrapped json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &env) == nil && len(env.Wrapped) > 0 {
		body = env.Wrapped
	}
	req, err := parseGemini(geminiBody(body, model, false))
	if err != nil {
		writeError(w, geminiClient, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"totalTokens": estimateTokens(req)})
}

// unknownModelMsg is the 404 text for a model dial does not know.
func unknownModelMsg(model string) string {
	msg := fmt.Sprintf("dial knows no model %q", model)
	if ids := provider.IDs(); len(ids) > 0 {
		msg += "; it has " + strings.Join(ids, ", ")
	} else {
		msg += "; add a provider in dial first"
	}
	return msg
}

// geminiBody gives a Gemini request the model and stream fields that the
// rest of the gateway reads from a body. In Gemini's API both live in the URL.
func geminiBody(body []byte, model string, stream bool) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	m["model"], m["stream"] = model, stream
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// estimateTokens is the countTokens guess: about four characters per token.
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

// ---- request side -----------------------------------------------------------------

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

type gFunc struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
}

type gRequest struct {
	Model             string     `json:"model"`
	Stream            bool       `json:"stream"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Contents          []gContent `json:"contents"`
	Tools             []struct {
		FunctionDeclarations []gFunc `json:"functionDeclarations"`
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

// parseGemini reads a Gemini request into the shared request form.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}
	if g.SystemInstruction != nil {
		r.System = geminiText(g.SystemInstruction.Parts)
	}

	var calls []Part // tool calls so far, for matching results by name
	nextID := 0
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					nextID++
					id = fmt.Sprintf("gcall_%d", nextID)
				}
				part := Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: geminiArgs(p.FunctionCall.Args)}
				calls = append(calls, part)
				msg.Parts = append(msg.Parts, part)
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = matchCall(calls, fr.Name)
				}
				out, isErr := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: isErr})
			case p.InlineData != nil:
				if strings.HasPrefix(p.InlineData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: attachmentNote(p.InlineData.MimeType)})
				}
			case p.FileData != nil:
				if strings.HasPrefix(p.FileData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: attachmentNote(p.FileData.MimeType)})
				}
			case p.Thought:
				if p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
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
		for _, f := range t.FunctionDeclarations {
			schema := f.Parameters
			if len(schema) == 0 {
				schema = f.ParametersJSONSchema
			}
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: lowerSchema(schema)})
		}
	}

	var allowed []string
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		allowed = fc.AllowedFunctionNames
		switch fc.Mode {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			r.ToolChoice = "required"
			if len(allowed) == 1 {
				r.ToolChoice = "name:" + allowed[0]
			}
		case "AUTO":
			r.ToolChoice = "auto"
		}
	}
	if len(allowed) > 0 {
		r.Tools = filterTools(r.Tools, allowed)
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			switch {
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget < 0:
				r.Effort = "medium" // the model decides; medium is the middle
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget > 0:
				r.Effort = effortOfBudget(*tc.ThinkingBudget)
			}
			r.Thinking = tc.IncludeThoughts || r.Effort != ""
		}
		if gc.ResponseMimeType == "application/json" || len(gc.ResponseSchema) > 0 || len(gc.ResponseJSONSchema) > 0 {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			r.System = joinSys(r.System, jsonInstruction(schema))
		}
	}
	return r, nil
}

// geminiText joins the text of the non-thought parts of a content.
func geminiText(parts []gPart) string {
	var out []string
	for _, p := range parts {
		if p.Text != "" && !p.Thought {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n")
}

// geminiArgs is a function call's arguments as a JSON object, never empty.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// matchCall finds the id of the most recent call of a function by name, for
// results that did not carry the call's id.
func matchCall(calls []Part, name string) string {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Name == name {
			return calls[i].ID
		}
	}
	return "call_" + name
}

// geminiResult reads a function result: its output, or its error.
func geminiResult(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return s, false
	}
	if e, ok := m["error"]; ok {
		return jsonText(e), true
	}
	if o, ok := m["output"]; ok {
		return jsonText(o), false
	}
	return s, false
}

// jsonText is a JSON string's value, or the raw JSON for anything else.
func jsonText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// attachmentNote stands in for a non-image attachment the upstream cannot take.
func attachmentNote(mime string) string {
	if mime == "" {
		mime = "unknown type"
	}
	return fmt.Sprintf("[attachment omitted: %s]", mime)
}

// lowerSchema turns Gemini's upper-case schema type names (OBJECT, STRING…)
// into standard lower-case JSON Schema types. Input it cannot read is kept.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
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
			} else {
				x[k] = lowerTypes(val)
			}
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

// filterTools keeps the declared tools the request allows.
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

// jsonInstruction asks for bare JSON, with the schema when there is one. It
// is used for every upstream, since the shared request has no JSON-mode field.
func jsonInstruction(schema json.RawMessage) string {
	const head = "Respond only with a single valid JSON value. Do not write any prose or code fences around it."
	if len(schema) == 0 {
		return head
	}
	return head + " It must conform to this JSON Schema:\n" + string(lowerSchema(schema))
}

func joinSys(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n\n" + b
}

// ---- response side ------------------------------------------------------------------

// geminiFinish maps a stop reason to Gemini's finishReason.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage maps token counts to usageMetadata. The prompt count includes
// cache hits; reasoning tokens are reported apart from the candidates.
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

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			parts = append(parts, map[string]any{"text": p.Text})
		case Thinking:
			parts = append(parts, map[string]any{"text": p.Text, "thought": true})
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}})
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
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(res.Stop), "index": 0}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
	return b
}

// geminiEncoder writes events as Gemini stream chunks. A function call is held
// until its arguments are complete, so a partial call is never sent.
type geminiEncoder struct {
	w        *sseWriter
	model    string
	id       string
	col      collector
	open     bool // a function call is being streamed
	callID   string
	callName string
	args     strings.Builder
}

func (e *geminiEncoder) responseID() string {
	if e.id == "" {
		e.id = "resp_" + newID()
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

func (e *geminiEncoder) flushCall() {
	if !e.open {
		return
	}
	e.open = false
	id := e.callID
	if id == "" {
		id = "call_" + newID()
	}
	e.send([]map[string]any{{"functionCall": map[string]any{"id": id, "name": e.callName, "args": parseArgs(e.args.String())}}})
	e.args.Reset()
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
	case KToolStart:
		e.flushCall()
		e.open, e.callID, e.callName = true, ev.ID, ev.Name
	case KToolArgs:
		if e.open {
			e.args.WriteString(ev.Text)
		}
	case KText:
		e.flushCall()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text}})
		}
	case KThink:
		e.flushCall()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text, "thought": true}})
		}
	case KError:
		e.flushCall()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
	e.col.add(ev)
}

// finish sends the closing chunk, with the finish reason and usage.
func (e *geminiEncoder) finish() {
	e.flushCall()
	res := e.col.finish()
	e.w.event("", map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": []any{}},
			"finishReason": geminiFinish(res.Stop), "index": 0}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  e.model,
		"responseId":    e.responseID(),
	})
}

// geminiStatus names an HTTP status the way the Gemini API does.
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
