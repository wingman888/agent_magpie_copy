package gateway

// The Gemini API, served to clients such as Gemini CLI. Like the other three
// client APIs, a request is parsed into the shared IR and sent to whichever
// upstream serves the model; the reply is rendered back in Gemini's shape.
// No upstream speaks Gemini, so it only ever appears on the client side.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// ---- request shapes -----------------------------------------------------------

// gInt is an integer Gemini clients may send as a number or as a string.
type gInt int

func (n *gInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "null" || s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*n = gInt(f)
	return nil
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

type gResult struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gPart struct {
	Text             string   `json:"text"`
	Thought          bool     `json:"thought"`
	InlineData       *gBlob   `json:"inlineData"`
	FileData         *gFile   `json:"fileData"`
	FunctionCall     *gCall   `json:"functionCall"`
	FunctionResponse *gResult `json:"functionResponse"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gFunctionDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
}

type gRequest struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Tools             []struct {
		FunctionDeclarations []gFunctionDecl `json:"functionDeclarations"`
	} `json:"tools"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *struct {
		MaxOutputTokens    gInt            `json:"maxOutputTokens"`
		Temperature        *float64        `json:"temperature"`
		TopP               *float64        `json:"topP"`
		StopSequences      []string        `json:"stopSequences"`
		ResponseMimeType   string          `json:"responseMimeType"`
		ResponseSchema     json.RawMessage `json:"responseSchema"`
		ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"`
		ThinkingConfig     *struct {
			ThinkingBudget  *gInt  `json:"thinkingBudget"`
			ThinkingLevel   string `json:"thinkingLevel"`
			IncludeThoughts bool   `json:"includeThoughts"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

// gEnvelope carries what the URL said (model, streaming) next to the body,
// because translate parses the body itself and has no other way to learn them.
type gEnvelope struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Body   json.RawMessage `json:"body"`
}

func geminiEnvelope(model string, stream bool, body []byte) []byte {
	b, _ := json.Marshal(gEnvelope{Model: model, Stream: stream, Body: body})
	return b
}

// parseGemini reads an envelope made by geminiEnvelope.
func parseGemini(body []byte) (*Request, error) {
	var env gEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r, err := parseGeminiBody(env.Model, env.Body)
	if err != nil {
		return nil, err
	}
	r.Stream = env.Stream
	return r, nil
}

// parseGeminiBody reads a generateContent request body into the IR.
func parseGeminiBody(model string, body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: model}
	if g.SystemInstruction != nil {
		r.System = geminiText(g.SystemInstruction.Parts)
	}

	// function name -> id of its latest call, for results that carry no id
	lastCall := map[string]string{}
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.Thought:
				if p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text})
				}
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + newID()
				}
				lastCall[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiArgs(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = lastCall[fr.Name]
				}
				if id == "" {
					id = "call_" + newID()
				}
				text, failed := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: failed})
			case p.InlineData != nil:
				if strings.HasPrefix(p.InlineData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, placeholder(p.InlineData.MimeType))
				}
			case p.FileData != nil:
				if strings.HasPrefix(p.FileData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, placeholder(p.FileData.MimeType))
				}
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}

	// tools, and which of them the client allows the model to call
	var mode string
	var allowed []string
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		mode = strings.ToUpper(g.ToolConfig.FunctionCallingConfig.Mode)
		allowed = g.ToolConfig.FunctionCallingConfig.AllowedFunctionNames
	}
	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.ParametersJSONSchema
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			if len(allowed) > 0 && !containsStr(allowed, fd.Name) {
				continue
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: geminiSchema(schema)})
		}
	}
	switch mode {
	case "NONE":
		r.ToolChoice = "none"
	case "ANY":
		if len(allowed) == 1 {
			r.ToolChoice = "name:" + allowed[0]
		} else {
			r.ToolChoice = "required"
		}
	case "AUTO":
		r.ToolChoice = "auto"
	}
	if len(r.Tools) == 0 && r.ToolChoice != "none" {
		r.ToolChoice = ""
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = int(gc.MaxOutputTokens)
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			r.Thinking = tc.IncludeThoughts
			switch {
			case tc.ThinkingBudget != nil && int(*tc.ThinkingBudget) == -1:
				r.Effort = "medium" // the model decides; medium is the middle ground
			case tc.ThinkingBudget != nil && *tc.ThinkingBudget > 0:
				r.Effort = effortOfBudget(int(*tc.ThinkingBudget))
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			}
			if r.Effort != "" {
				r.Thinking = true
			}
		}
		// Gemini's JSON mode has no counterpart in the IR, so the request
		// asks for JSON in words, with the schema attached.
		schema := gc.ResponseJSONSchema
		if len(schema) == 0 {
			schema = gc.ResponseSchema
		}
		if strings.EqualFold(gc.ResponseMimeType, "application/json") || len(schema) > 0 {
			note := "Respond only with a single valid JSON value: no prose, no comments, no markdown fences."
			if s := geminiSchema(schema); len(s) > 0 {
				note += "\nThe JSON must conform to this JSON Schema:\n" + string(s)
			}
			r.System = joinNonEmpty("\n\n", r.System, note)
		}
	}
	return r, nil
}

func placeholder(mime string) Part {
	if mime == "" {
		mime = "unknown type"
	}
	return Part{Kind: Text, Text: fmt.Sprintf("[attachment omitted: %s]", mime)}
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func joinNonEmpty(sep string, xs ...string) string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return strings.Join(out, sep)
}

// geminiText joins the text parts of a Gemini content.
func geminiText(parts []gPart) string {
	var out []string
	for _, p := range parts {
		if p.Text != "" && !p.Thought {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n\n")
}

// geminiArgs makes a function call's arguments a JSON object, as the IR wants.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(s)) && strings.HasPrefix(s, "{") {
		return json.RawMessage(s)
	}
	return parseArgs(s)
}

// geminiResult reads a function response. Gemini clients put the output under
// "output" or, on failure, under "error"; anything else is passed on as JSON.
func geminiResult(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil {
		if e, ok := obj["error"]; ok && len(e) > 0 {
			return stringOrJSON(e), true
		}
		if o, ok := obj["output"]; ok && len(o) > 0 {
			return stringOrJSON(o), false
		}
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

// geminiSchema turns a Gemini schema into a standard JSON Schema: type names
// such as OBJECT become object, at every depth.
func geminiSchema(raw json.RawMessage) json.RawMessage {
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
	b, err := json.Marshal(lowerTypes(v))
	if err != nil {
		return raw
	}
	return b
}

func lowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "propertyOrdering") // Gemini-only keyword
		nullable, _ := x["nullable"].(bool)
		delete(x, "nullable")
		for k, val := range x {
			if k == "type" {
				switch t := val.(type) {
				case string:
					x[k] = strings.ToLower(t)
					if nullable {
						x[k] = []any{strings.ToLower(t), "null"}
					}
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
		return x
	case []any:
		for i := range x {
			x[i] = lowerTypes(x[i])
		}
		return x
	}
	return v
}

// ---- response shapes ----------------------------------------------------------

// geminiFinish is the finishReason for a stop.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage is usageMetadata. The IR counts reasoning inside Output, so
// candidates are Output less reasoning; prompt includes cache reads.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	m := map[string]any{
		"promptTokenCount":     prompt,
		"candidatesTokenCount": cand,
		"totalTokenCount":      prompt + cand + u.Reasoning,
	}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	return m
}

// geminiPart is one output part, or nil when the reply has nothing to show.
func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Text:
		if p.Text == "" {
			return nil
		}
		return map[string]any{"text": p.Text}
	case Thinking:
		if p.Text == "" {
			return nil
		}
		return map[string]any{"text": p.Text, "thought": true}
	case ToolCall:
		id := p.ID
		if id == "" {
			id = "call_" + newID()
		}
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": geminiArgs(argsOf(p))}}
	}
	return nil
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		if pm := geminiPart(p); pm != nil {
			parts = append(parts, pm)
		}
	}
	id := res.ID
	if id == "" {
		id = "resp-" + newID()
	}
	if res.Model != "" {
		model = res.Model
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

// geminiEncoder writes events as a Gemini SSE stream. A function call is
// held back until it is complete, so a client never sees half of one.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector

	open     bool
	callID   string
	callName string
	args     strings.Builder
}

func newGeminiEncoder(w *sseWriter, model string) *geminiEncoder {
	return &geminiEncoder{w: w, model: model, id: "resp-" + newID()}
}

func (e *geminiEncoder) send(parts []map[string]any, finish string, meta map[string]any) {
	if parts == nil {
		parts = []map[string]any{}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	chunk := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": e.model, "responseId": e.id}
	if meta != nil {
		chunk["usageMetadata"] = meta
	}
	e.w.event("", chunk)
}

// closeTool sends the open function call, whole, if there is one.
func (e *geminiEncoder) closeTool() {
	if !e.open {
		return
	}
	e.open = false
	args := geminiArgs(json.RawMessage(e.args.String()))
	e.args.Reset()
	e.send([]map[string]any{{"functionCall": map[string]any{"id": e.callID, "name": e.callName, "args": args}}}, "", nil)
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
		e.closeTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text}}, "", nil)
		}
	case KThink:
		e.closeTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text, "thought": true}}, "", nil)
		}
	case KToolStart:
		e.closeTool()
		e.open = true
		e.callID, e.callName = ev.ID, ev.Name
		if e.callID == "" {
			e.callID = "call_" + newID()
		}
	case KToolArgs:
		if e.open {
			e.args.WriteString(ev.Text)
		}
	case KError:
		e.closeTool()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.closeTool()
	res := e.col.finish()
	e.send(nil, geminiFinish(res.Stop), geminiUsage(res.Usage))
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

// ---- routes -------------------------------------------------------------------

// unknownModel is the message for a model dial does not know.
func unknownModel(id string) string {
	msg := fmt.Sprintf("dial knows no model %q", id)
	if ids := provider.IDs(); len(ids) > 0 {
		msg += "; it has " + strings.Join(ids, ", ")
	} else {
		msg += "; add a provider in dial first"
	}
	return msg
}

// gemini serves POST /v1beta/models/{model}:{method}. The model can hold
// slashes, so the method is whatever follows the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, provider.Gemini, 404, "dial serves the Gemini methods generateContent, streamGenerateContent and countTokens")
		return
	}
	model, method := strings.TrimPrefix(rest[:i], "models/"), rest[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	switch method {
	case "generateContent":
		s.geminiGenerate(w, r, model, false, body)
	case "streamGenerateContent":
		s.geminiGenerate(w, r, model, true, body)
	case "countTokens":
		s.geminiCount(w, model, body)
	default:
		writeError(w, provider.Gemini, 404, fmt.Sprintf("dial has no Gemini method %q", method))
	}
}

// geminiGenerate is the request path of generateContent and its streaming form.
// It mirrors handle: resolve the model, translate, record the call, count usage.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, model string, stream bool, body []byte) {
	start := time.Now()
	call := Call{Time: start, From: provider.Gemini, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, upstreamModel, ok := provider.Resolve(model)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeError(w, provider.Gemini, 404, unknownModel(model))
		return
	}
	call.Provider = p.ID
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, provider.Gemini, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	if !json.Valid(body) {
		call.Status, call.Error = 400, "invalid request: body is not JSON"
		s.record(call)
		writeError(w, provider.Gemini, 400, call.Error)
		return
	}
	call.Status, call.Error = s.translate(w, r, p, provider.Gemini, to[0], upstreamModel, geminiEnvelope(upstreamModel, stream, body), &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: upstreamModel,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiCount answers countTokens with a rough estimate: about four bytes a
// token. The body is either the request itself or wraps it in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, provider.Gemini, 404, unknownModel(model))
		return
	}
	raw := body
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 {
		raw = wrap.GenerateContentRequest
	}
	req, err := parseGeminiBody(model, raw)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
			if p.Kind == Image {
				n += 1000 // images cost a roughly fixed amount
			}
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	writeJSON(w, 200, map[string]any{"totalTokens": n / 4})
}

// geminiModelObject describes one catalog model the Gemini way.
func geminiModelObject(e provider.Entry) map[string]any {
	return map[string]any{
		"name":                       "models/" + e.ID,
		"version":                    "001",
		"displayName":                e.Name,
		"description":                e.Name + " via dial",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
	}
}

// geminiModels is GET /v1beta/models: every catalog model.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiModel is GET /v1beta/models/{model}.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("rest"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, unknownModel(id))
}
