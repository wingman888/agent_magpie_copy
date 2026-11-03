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

// ---- Gemini API (served to clients; upstreams never speak it) ----------------
//
// A Gemini call names its model and method in the URL:
//   POST /v1beta/models/{model}:generateContent
//   POST /v1beta/models/{model}:streamGenerateContent?alt=sse
//   POST /v1beta/models/{model}:countTokens
// The handler folds those two into the body (as "model" and "stream") and
// hands the request to the same handle() path every other client API uses.
// From there the request is parsed into the shared Request, routed like any
// other, and the reply is rendered back in Gemini's shape.

// gBlob is inline data (inlineData) or a function response's binary part.
type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFileData struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gResponse struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gPart struct {
	Text             string     `json:"text"`
	Thought          bool       `json:"thought"`
	InlineData       *gBlob     `json:"inlineData"`
	FileData         *gFileData `json:"fileData"`
	FunctionCall     *gCall     `json:"functionCall"`
	FunctionResponse *gResponse `json:"functionResponse"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gRequest struct {
	Model             string     `json:"model"`
	Stream            bool       `json:"stream"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Contents          []gContent `json:"contents"`
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

// parseGemini reads a Gemini request (with "model" and "stream" injected by
// the URL handler) into the shared Request.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}
	var sys []string
	if g.SystemInstruction != nil {
		var b strings.Builder
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != "" && !p.Thought {
				b.WriteString(p.Text)
			}
		}
		if s := b.String(); s != "" {
			sys = append(sys, s)
		}
	}

	// Function calls get ids when the client did not send any, and each
	// function response without an id is matched to the latest call of the
	// same name.
	calls := map[string][]string{}
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
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			case p.InlineData != nil:
				if mt := p.InlineData.MimeType; strings.HasPrefix(mt, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment omitted: %s]", mt)})
				}
			case p.FileData != nil:
				if mt := p.FileData.MimeType; strings.HasPrefix(mt, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file omitted: %s]", p.FileData.FileURI)})
				}
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + newID()
				}
				calls[fc.Name] = append(calls[fc.Name], id)
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: parseArgs(string(fc.Args))})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					if ids := calls[fr.Name]; len(ids) > 0 {
						id = ids[len(ids)-1]
					} else {
						id = "call_" + newID()
					}
				}
				text, isErr := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}

	if g.GenerationConfig != nil {
		gc := g.GenerationConfig
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp = gc.Temperature
		r.TopP = gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			r.Thinking = tc.IncludeThoughts
			switch {
			case tc.ThinkingBudget != nil:
				// 0 switches reasoning off; -1 leaves the depth to the model,
				// which we map to medium; positive budgets go through the
				// shared budget-to-effort table.
				switch b := *tc.ThinkingBudget; {
				case b < 0:
					r.Effort = "medium"
				case b > 0:
					r.Effort = effortOfBudget(b)
				}
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			}
			if r.Effort != "" {
				r.Thinking = true
			}
		}
		schema := gc.ResponseJSONSchema
		if len(schema) == 0 {
			schema = gc.ResponseSchema
		}
		if strings.EqualFold(gc.ResponseMimeType, "application/json") || len(schema) > 0 {
			// Not every upstream has a JSON mode, so the instruction is
			// always stated in the system prompt, schema included.
			sys = append(sys, geminiJSONInstruction(lowerSchema(schema)))
		}
	}
	r.System = strings.Join(sys, "\n\n")

	var allowed []string
	mode := ""
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		mode = strings.ToUpper(tc.FunctionCallingConfig.Mode)
		allowed = tc.FunctionCallingConfig.AllowedFunctionNames
	}
	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			if mode == "ANY" && len(allowed) > 0 && !geminiHas(allowed, fd.Name) {
				continue
			}
			schema := fd.Parameters
			if len(schema) == 0 {
				schema = fd.ParametersJSONSchema
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: lowerSchema(schema)})
		}
	}
	switch mode {
	case "NONE":
		r.ToolChoice = "none"
	case "AUTO":
		r.ToolChoice = "auto"
	case "ANY":
		if len(allowed) == 1 {
			r.ToolChoice = "name:" + allowed[0]
		} else {
			r.ToolChoice = "required"
		}
	}
	return r, nil
}

func geminiHas(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// geminiResult reads a function response's payload. The usual shape is
// {"output": "..."}; an {"error": ...} payload marks the result as failed.
func geminiResult(raw json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && m != nil {
		if out, ok := m["output"]; ok && len(m) == 1 {
			return rawText(out), false
		}
		if e, ok := m["error"]; ok {
			return rawText(e), true
		}
	}
	return string(raw), false
}

// rawText is a JSON string's content, or the raw JSON when it is not one.
func rawText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func geminiJSONInstruction(schema json.RawMessage) string {
	s := "Respond only with a single valid JSON value. Do not add prose, explanations or markdown code fences."
	if len(schema) > 0 {
		s += " The JSON must conform to this JSON schema:\n" + string(schema)
	}
	return s
}

// lowerSchema turns Gemini's upper-case schema type names (OBJECT, STRING…)
// into standard lower-case JSON Schema types, recursively.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
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
	b, err := json.Marshal(lowerTypes(v))
	if err != nil {
		return raw
	}
	return b
}

func lowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "type" {
				switch t := val.(type) {
				case string:
					x[k] = strings.ToLower(t)
					continue
				case []any:
					for i, e := range t {
						if s, ok := e.(string); ok {
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

// ---- HTTP handlers -------------------------------------------------------------

// gemini routes POST /v1beta/models/{model}:{method}.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("rest")
	i := strings.LastIndex(name, ":")
	if i <= 0 {
		writeError(w, provider.Gemini, 404, geminiPaths)
		return
	}
	model, method := name[:i], name[i+1:]
	switch method {
	case "generateContent", "streamGenerateContent":
		s.geminiGenerate(w, r, model, method == "streamGenerateContent")
	case "countTokens":
		s.geminiCount(w, r, model)
	default:
		writeError(w, provider.Gemini, 404, fmt.Sprintf("unknown method %q; %s", method, geminiPaths))
	}
}

const geminiPaths = "dial serves /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens"

// geminiGenerate folds the model and stream flag into the body and lets the
// shared handler take it from there. Streams are always SSE.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, model string, stream bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	env := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &env); err != nil || env == nil {
		msg := "request body must be a JSON object"
		if err != nil {
			msg = err.Error()
		}
		writeError(w, provider.Gemini, 400, "invalid request: "+msg)
		return
	}
	mb, _ := json.Marshal(model)
	env["model"] = mb
	env["stream"] = json.RawMessage("false")
	if stream {
		env["stream"] = json.RawMessage("true")
	}
	out, _ := json.Marshal(env)
	r.Body = io.NopCloser(bytes.NewReader(out))
	r.ContentLength = int64(len(out))
	s.handle(provider.Gemini)(w, r)
}

// geminiCount answers countTokens with a rough estimate: a quarter of the
// characters the request carries. The body may be the request itself or
// wrap it in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, model string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	var env struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	json.Unmarshal(body, &env)
	src := body
	if len(env.GenerateContentRequest) > 0 && string(env.GenerateContentRequest) != "null" {
		src = env.GenerateContentRequest
	}
	req, err := parseGemini(src)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, provider.Gemini, 404, fmt.Sprintf("dial knows no model %q", model))
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

// geminiModels lists the catalog in Gemini's model shape.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, e := range provider.Catalog() {
		data = append(data, geminiModel(e))
	}
	writeJSON(w, 200, map[string]any{"models": data})
}

// geminiModel answers GET /v1beta/models/{name} for one catalog model.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.PathValue("rest"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == name {
			writeJSON(w, 200, geminiModel(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, fmt.Sprintf("models/%s is not found", name))
}

func geminiModel(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "baseModelId": e.ID, "version": "1",
		"displayName": name, "description": name,
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}

// ---- rendering replies ----------------------------------------------------------

// geminiFinish maps a reply's stop reason to Gemini's finishReason.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiStatus is the gRPC-style status name Gemini puts in errors.
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
	case code == 502, code == 503, code == 529:
		return "UNAVAILABLE"
	case code >= 500:
		return "INTERNAL"
	}
	return "UNKNOWN"
}

// gemini is Gemini's usage block. The shared Usage counts reasoning inside
// Output (as Chat does), so the candidate count leaves it out; the prompt
// count includes cache reads.
func (u Usage) gemini() map[string]any {
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
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(res.Stop), "index": 0}},
		"usageMetadata": res.Usage.gemini(),
		"modelVersion":  model,
		"responseId":    id,
	})
	return b
}

// geminiEncoder writes events as a Gemini SSE stream. A function call is
// held back until it is complete and then sent whole, so a client never sees
// half the arguments. The final event carries finishReason and usage.
type geminiEncoder struct {
	w       *sseWriter
	model   string
	id      string
	started bool
	stop    string
	usage   Usage
	failed  bool
	open    bool // a function call is being received
	callID  string
	callNam string
	args    strings.Builder
}

func (e *geminiEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started = true
	e.id = ev.MsgID
	if e.id == "" {
		e.id = "resp_" + newID()
	}
	if ev.Model != "" {
		e.model = ev.Model
	}
}

func (e *geminiEncoder) emit(parts []map[string]any, finish string, usage map[string]any) {
	if parts == nil {
		parts = []map[string]any{}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	out := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": e.model, "responseId": e.id}
	if usage != nil {
		out["usageMetadata"] = usage
	}
	e.w.event("", out)
}

func (e *geminiEncoder) flushCall() {
	if !e.open {
		return
	}
	e.open = false
	fc := map[string]any{"id": e.callID, "name": e.callNam, "args": parseArgs(e.args.String())}
	e.emit([]map[string]any{{"functionCall": fc}}, "", nil)
	e.args.Reset()
	e.callID, e.callNam = "", ""
}

func (e *geminiEncoder) event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		e.flushCall()
		if ev.Text != "" {
			e.emit([]map[string]any{{"text": ev.Text}}, "", nil)
		}
	case KThink:
		e.flushCall()
		if ev.Text != "" {
			e.emit([]map[string]any{{"text": ev.Text, "thought": true}}, "", nil)
		}
	case KToolStart:
		e.flushCall()
		e.open = true
		e.callID, e.callNam = ev.ID, ev.Name
		if e.callID == "" {
			e.callID = "call_" + newID()
		}
	case KToolArgs:
		if e.open {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushCall()
		e.stop = ev.Stop
	case KUsage:
		e.usage.add(ev.Usage)
	case KError:
		e.flushCall()
		e.failed = true
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
}

func (e *geminiEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.flushCall()
	if e.failed {
		return
	}
	e.emit(nil, geminiFinish(e.stop), e.usage.gemini())
}
