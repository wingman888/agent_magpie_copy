package gateway

// The Gemini REST API, served as a client protocol. A Gemini CLI, or any
// client that speaks the Gemini API, can use every model in the catalog
// through it. Gemini is never an upstream: a request is read into the same
// Request every other client produces, and the provider is reached over the
// wire API it speaks.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// geminiMethods are the generation methods dial serves, as the Gemini API
// lists them for a model.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// ---- routes -----------------------------------------------------------------

// gemini serves POST /v1beta/models/{model}:{method}. The model is a catalog
// reference such as provider/model, which may itself contain colons, so the
// method is whatever follows the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	name, method := "", ""
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		name, method = rest[:i], rest[i+1:]
	}
	if !slices.Contains(geminiMethods, method) {
		writeError(w, provider.Gemini, 404, "dial serves /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens, not "+rest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	if method == "countTokens" {
		s.geminiCount(w, name, body)
		return
	}
	start := time.Now()
	call := Call{Time: start, From: provider.Gemini, Model: name, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(name)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeError(w, provider.Gemini, 404, unknownModel(name))
		return
	}
	call.Provider = p.ID
	req, err := parseGemini(body)
	if err != nil {
		call.Status, call.Error = 400, err.Error()
		s.record(call)
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	req.Model, req.Stream = name, method == "streamGenerateContent"
	to, ok := upstreamOf(p)
	if !ok {
		writeError(w, provider.Gemini, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to
	call.Status, call.Error = s.send(w, r, p, provider.Gemini, to, model, req, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.finish(call, model)
}

// upstreamOf is the wire API dial speaks to a provider on behalf of a client
// whose own protocol the provider does not serve.
func upstreamOf(p provider.Provider) (provider.Protocol, bool) {
	if provider.IsAccount(p.ID) {
		return signInProtocol(p.ID), true
	}
	to := p.Speaks()
	if len(to) == 0 {
		return "", false
	}
	return to[0], true
}

// geminiCount estimates the tokens of a request, as countTokens does for
// Anthropic. The body is either a generate request or one wrapped in
// generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, name string, body []byte) {
	if _, _, ok := provider.Resolve(name); !ok {
		writeError(w, provider.Gemini, 404, unknownModel(name))
		return
	}
	var wrap struct {
		Request json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Request) > 0 {
		body = wrap.Request
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, pt := range m.Parts {
			n += len(pt.Text) + len(pt.Args) + len(pt.Name)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	writeJSON(w, 200, map[string]any{"totalTokens": n / 4})
}

// geminiModels answers GET /v1beta/models with the catalog.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiModel answers GET /v1beta/models/{model} for one catalog model.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("rest")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, unknownModel(id))
}

func geminiModelObject(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "displayName": name, "supportedGenerationMethods": geminiMethods}
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

// ---- request ------------------------------------------------------------------

type gPart struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	ThoughtSignature string `json:"thoughtSignature"`
	FunctionCall     *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData"`
	FileData *struct {
		MimeType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

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
			ThinkingBudget *int   `json:"thinkingBudget"`
			ThinkingLevel  string `json:"thinkingLevel"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

// gCalls gives function calls and their responses ids. A call without an id
// gets one; a response without an id pairs with the latest call of its name.
type gCalls struct {
	n    int
	last map[string]string
}

func (c *gCalls) fresh() string {
	c.n++
	return fmt.Sprintf("call_gemini_%d", c.n)
}

func (c *gCalls) call(id, name string) string {
	if id == "" {
		id = c.fresh()
	}
	c.last[name] = id
	return id
}

func (c *gCalls) response(id, name string) string {
	if id != "" {
		return id
	}
	if prev, ok := c.last[name]; ok {
		return prev
	}
	return c.fresh()
}

// geminiTypes are the upper-case schema types Gemini uses; the other APIs
// want them lower-cased.
var geminiTypes = map[string]bool{"OBJECT": true, "STRING": true, "INTEGER": true, "NUMBER": true,
	"BOOLEAN": true, "ARRAY": true, "NULL": true}

// parseGemini reads a Gemini generate request into the shared Request. The
// caller sets Model and Stream, which the URL carries rather than the body.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	if g.SystemInstruction != nil {
		r.System = gText(g.SystemInstruction.Parts)
	}
	calls := &gCalls{last: map[string]string{}}
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			msg.Parts = append(msg.Parts, gPartOf(p, calls)...)
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
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: schemaLower(schema)})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		f := tc.FunctionCallingConfig
		switch strings.ToUpper(f.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		case "ANY":
			r.ToolChoice = "required"
			if len(f.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + f.AllowedFunctionNames[0]
			}
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			schema := gc.ResponseSchema
			if len(schema) == 0 {
				schema = gc.ResponseJSONSchema
			}
			r.System = joinSystem(r.System, jsonInstruction(schemaLower(schema)))
		}
		if tc := gc.ThinkingConfig; tc != nil {
			r.Effort = gEffort(tc.ThinkingBudget, tc.ThinkingLevel)
			r.Thinking = r.Effort != ""
		}
	}
	return r, nil
}

// gPartOf reads one part of a Gemini content into the parts it stands for.
func gPartOf(p gPart, calls *gCalls) []Part {
	switch {
	case p.FunctionCall != nil:
		f := p.FunctionCall
		return []Part{{Kind: ToolCall, ID: calls.call(f.ID, f.Name), Name: f.Name, Args: parseArgs(string(f.Args))}}
	case p.FunctionResponse != nil:
		f := p.FunctionResponse
		out, failed := gResponse(f.Response)
		return []Part{{Kind: ToolResult, CallID: calls.response(f.ID, f.Name), Text: out, IsError: failed}}
	case p.InlineData != nil:
		return gFile(p.InlineData.MimeType, Part{Data: p.InlineData.Data})
	case p.FileData != nil:
		return gFile(p.FileData.MimeType, Part{URL: p.FileData.FileURI})
	case p.Thought:
		return []Part{{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature}}
	case p.Text != "":
		return []Part{{Kind: Text, Text: p.Text}}
	}
	return nil
}

// gFile keeps an image as an image; any other file is named in the text,
// since the wire APIs cannot carry it.
func gFile(mime string, p Part) []Part {
	if strings.HasPrefix(mime, "image/") {
		if p.URL == "" && p.Data == "" {
			return nil
		}
		p.Kind, p.MediaType = Image, mime
		return []Part{p}
	}
	if mime == "" {
		mime = "unknown"
	}
	return []Part{{Kind: Text, Text: "[attachment: " + mime + "]"}}
}

// gResponse reads a function response. Gemini's convention is an object; an
// "output" string is the result, an "error" string is a failed call, and any
// other object is passed on as JSON.
func gResponse(raw json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if s, ok := gStr(m, "output"); ok {
			return s, false
		}
		if s, ok := gStr(m, "error"); ok {
			return s, true
		}
	}
	return string(raw), false
}

func gStr(m map[string]json.RawMessage, key string) (string, bool) {
	var s string
	if json.Unmarshal(m[key], &s) != nil {
		return "", false
	}
	return s, true
}

// gText joins the text of the parts that are not thoughts.
func gText(parts []gPart) string {
	var out []string
	for _, p := range parts {
		if p.Text != "" && !p.Thought {
			out = append(out, p.Text)
		}
	}
	return strings.Join(out, "\n\n")
}

// gEffort reads a Gemini thinking setting as an effort level. A budget of 0
// turns thinking off; -1 leaves the choice to the model, taken as medium.
func gEffort(budget *int, level string) string {
	if budget == nil {
		return effortOf(level)
	}
	n := *budget
	switch {
	case n == 0:
		return ""
	case n < 0:
		return "medium"
	case n <= 4096:
		return "low"
	case n <= 12000:
		return "medium"
	}
	return "high"
}

// jsonInstruction asks for a JSON-only reply, since the wire APIs have no
// field for Gemini's response MIME type.
func jsonInstruction(schema json.RawMessage) string {
	s := "Reply only with a JSON value, with no prose or code fence around it."
	if len(schema) > 0 {
		s += " The JSON must conform to this schema: " + string(schema)
	}
	return s
}

func joinSystem(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

// schemaLower lower-cases Gemini's upper-case schema types, recursively.
func schemaLower(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(lowerTypeNames(v))
	if err != nil {
		return raw
	}
	return out
}

func lowerTypeNames(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && k == "type" && geminiTypes[s] {
				x[k] = strings.ToLower(s)
			} else {
				x[k] = lowerTypeNames(e)
			}
		}
	case []any:
		for i, e := range x {
			x[i] = lowerTypeNames(e)
		}
	}
	return v
}

// ---- reply --------------------------------------------------------------------

// gFinish is the Gemini finish reason for a reply's stop.
func gFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// gUsage reports the token counts in Gemini's terms. Output counts what the
// provider reported, reasoning included, as thoughtsTokenCount.
func gUsage(u Usage) map[string]any {
	in := u.Input + u.CacheRead + u.CacheWrite
	out := map[string]any{"promptTokenCount": in, "candidatesTokenCount": u.Output,
		"thoughtsTokenCount": u.Reasoning, "totalTokenCount": in + u.Output}
	if u.CacheRead > 0 {
		out["cachedContentTokenCount"] = u.CacheRead
	}
	return out
}

// gParts renders the parts of a reply in Gemini's shape.
func gParts(parts []Part) []any {
	out := []any{}
	for _, p := range parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text == "" && p.Signature == "" {
				continue
			}
			m := map[string]any{"text": p.Text, "thought": true}
			if p.Signature != "" {
				m["thoughtSignature"] = p.Signature
			}
			out = append(out, m)
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			out = append(out, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}})
		}
	}
	return out
}

// gEnd is the candidate and identity part of a reply, shared by the whole
// reply and the last event of a stream.
func gEnd(res Result, model string, parts []any) map[string]any {
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	if res.Model != "" {
		model = res.Model
	}
	return map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": gFinish(res.Stop),
			"index":        0,
		}},
		"usageMetadata": gUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	}
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	b, _ := json.Marshal(gEnd(res, model, gParts(res.Parts)))
	return b
}

// geminiEncoder writes events as a Gemini stream: one part per event, in
// the order the reply produced them. A function call is held until its
// arguments are complete, since Gemini sends each call whole. The last event
// carries the finish reason and usage.
type geminiEncoder struct {
	w     *sseWriter
	model string
	open  bool // a function call is being streamed
	id    string
	name  string
	args  strings.Builder
	col   collector
}

func (e *geminiEncoder) send(part map[string]any) {
	e.w.event("", map[string]any{"candidates": []map[string]any{{
		"content": map[string]any{"role": "model", "parts": []any{part}},
	}}})
}

// flushCall sends the function call that was being streamed, if any.
func (e *geminiEncoder) flushCall() {
	if !e.open {
		return
	}
	id := e.id
	if id == "" {
		id = "call_" + newID()
	}
	e.send(map[string]any{"functionCall": map[string]any{"id": id, "name": e.name, "args": parseArgs(e.args.String())}})
	e.open, e.id, e.name = false, "", ""
	e.args.Reset()
}

func (e *geminiEncoder) event(ev Event) {
	if ev.Kind != KToolArgs {
		e.flushCall()
	}
	switch ev.Kind {
	case KText:
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text})
		}
	case KThink:
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text, "thought": true})
		}
	case KSig:
		if ev.Text != "" {
			e.send(map[string]any{"thought": true, "thoughtSignature": ev.Text})
		}
	case KToolStart:
		e.open, e.id, e.name = true, ev.ID, ev.Name
	case KToolArgs:
		if e.open {
			e.args.WriteString(ev.Text)
		}
	case KError:
		e.w.event("", map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushCall()
	res := e.col.finish()
	e.w.event("", gEnd(res, e.model, []any{}))
}
