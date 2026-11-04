package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// The Gemini API is served to clients such as Gemini CLI and never spoken
// upstream. A Gemini call goes through the same translate path as the other
// three APIs: it is parsed into the shared Request, sent to whichever wire
// API the chosen provider speaks, and the reply is rendered back as Gemini.
// Gemini carries the model and the stream flag in the URL, so the handler
// writes them into the body under the same keys the other APIs use.

// geminiProto is the Gemini API as a client-facing protocol. No provider
// lists it in Protocols, so it never takes the passthrough path.
const geminiProto = provider.Protocol("gemini")

var callSeq atomic.Uint64

// geminiCallID makes an id for a function call that arrived without one.
func geminiCallID() string {
	return fmt.Sprintf("call_%s_%d", newID(), callSeq.Add(1))
}

// geminiRespID is the response id: the upstream's when it gave one.
func geminiRespID(id string) string {
	if id == "" {
		return "dial-" + newID()
	}
	return id
}

// geminiUnknownModel is the 404 text for a model dial does not know.
func geminiUnknownModel(model string) string {
	msg := fmt.Sprintf("dial knows no model %q", model)
	if ids := provider.IDs(); len(ids) > 0 {
		msg += "; it has " + strings.Join(ids, ", ")
	} else {
		msg += "; add a provider in dial first"
	}
	return msg
}

// geminiStatus is the gRPC-style status name Gemini uses for an HTTP code.
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
	case code == 502 || code == 503 || code == 529:
		return "UNAVAILABLE"
	case code == 504:
		return "DEADLINE_EXCEEDED"
	case code >= 500:
		return "INTERNAL"
	}
	return "UNKNOWN"
}

// ---- routes -------------------------------------------------------------------

func geminiModelObject(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "version": Version, "displayName": name,
		"description":                name + " via dial (" + e.Provider.Name + ")",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}

// geminiModels lists the whole catalog as Gemini's model list.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiModel answers GET /v1beta/models/{model}.
func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("rest")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, geminiProto, 404, "models/"+id+" is not found")
}

// geminiPost routes POST /v1beta/models/{model}:{method}. Model ids may
// contain slashes and colons, so the method is whatever follows the last
// colon.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, geminiProto, 404, "dial's Gemini API serves models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	model, method := rest[:i], rest[i+1:]
	switch method {
	case "generateContent":
		s.geminiGenerate(w, r, model, false)
	case "streamGenerateContent":
		s.geminiGenerate(w, r, model, true)
	case "countTokens":
		s.geminiCount(w, r, model)
	default:
		writeError(w, geminiProto, 404, "unknown method "+method+" on "+model)
	}
}

// geminiGenerate is the request path of generateContent and
// streamGenerateContent. It mirrors handle, which serves the other APIs.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, model string, stream bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	start := time.Now()
	call := Call{Time: start, From: geminiProto, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, vmodel, ok := provider.Resolve(model)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeError(w, geminiProto, 404, geminiUnknownModel(model))
		return
	}
	call.Provider = p.ID
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, geminiProto, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.translate(w, r, p, geminiProto, to[0], vmodel, geminiEnvelope(body, model, stream), &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: vmodel,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiCount answers countTokens with a rough estimate, about four
// characters to a token, counted over the same parts parseGemini reads.
func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request, model string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, geminiProto, 404, geminiUnknownModel(model))
		return
	}
	var wrap struct {
		Inner json.RawMessage `json:"generateContentRequest"`
	}
	json.Unmarshal(body, &wrap)
	if len(wrap.Inner) > 0 {
		body = wrap.Inner
	}
	req, err := parseGemini(geminiEnvelope(body, model, false))
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
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
	total := n / 4
	if total < 1 {
		total = 1
	}
	writeJSON(w, 200, map[string]any{"totalTokens": total})
}

// geminiEnvelope adds the model and stream flag, which Gemini keeps in the
// URL, to the body. A body that is not a JSON object is returned unchanged
// and fails in parseGemini with an error the client can read.
func geminiEnvelope(body []byte, model string, stream bool) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	m["model"] = model
	m["stream"] = stream
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- request ------------------------------------------------------------------

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`    // inlineData, base64
	FileURI  string `json:"fileUri"` // fileData
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
	ThoughtSignature string   `json:"thoughtSignature"`
	InlineData       *gBlob   `json:"inlineData"`
	FileData         *gBlob   `json:"fileData"`
	FunctionCall     *gCall   `json:"functionCall"`
	FunctionResponse *gResult `json:"functionResponse"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gFuncDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
}

type gThinking struct {
	ThinkingBudget  *int   `json:"thinkingBudget"`
	ThinkingLevel   string `json:"thinkingLevel"`
	IncludeThoughts bool   `json:"includeThoughts"`
}

type gGenConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"topP"`
	StopSequences      []string        `json:"stopSequences"`
	ResponseMimeType   string          `json:"responseMimeType"`
	ResponseSchema     json.RawMessage `json:"responseSchema"`
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"`
	ThinkingConfig     *gThinking      `json:"thinkingConfig"`
}

type gRequest struct {
	Model             string     `json:"model"`
	Stream            bool       `json:"stream"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Contents          []gContent `json:"contents"`
	Tools             []struct {
		FunctionDeclarations []gFuncDecl `json:"functionDeclarations"`
	} `json:"tools"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *gGenConfig `json:"generationConfig"`
}

// geminiCallRef remembers a function call so a result that carries no id
// can be matched to the latest call of the same name.
type geminiCallRef struct {
	name, id string
}

// parseGemini reads a generateContent body (with the envelope fields
// geminiEnvelope adds) into the shared Request.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}
	var sys []string
	if si := g.SystemInstruction; si != nil {
		if s := geminiText(si.Parts); s != "" {
			sys = append(sys, s)
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			r.Effort = geminiEffort(tc.ThinkingBudget, tc.ThinkingLevel)
			r.Thinking = tc.IncludeThoughts
		}
		// Upstreams have no notion of a Gemini JSON mode, so ask in words.
		schema := gc.ResponseJSONSchema
		if len(schema) == 0 {
			schema = gc.ResponseSchema
		}
		if gc.ResponseMimeType == "application/json" || len(schema) > 0 {
			sys = append(sys, jsonInstruction(schema))
		}
	}
	if r.Effort != "" {
		r.Thinking = true
	}
	var refs []geminiCallRef
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
					id = geminiCallID()
				}
				refs = append(refs, geminiCallRef{name: fc.Name, id: id})
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiArgs(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = refID(refs, fr.Name)
				}
				text, failed := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: failed})
			case p.Thought:
				if p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
				}
			case p.InlineData != nil:
				msg.Parts = append(msg.Parts, geminiInline(p.InlineData))
			case p.FileData != nil:
				msg.Parts = append(msg.Parts, geminiFile(p.FileData))
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
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
		mode := strings.ToUpper(fc.Mode)
		if mode == "" && len(fc.AllowedFunctionNames) > 0 {
			mode = "ANY"
		}
		switch mode {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO":
			r.ToolChoice = "auto"
		case "ANY":
			// several allowed names cannot be expressed upstream; "required"
			// then lets the model pick any tool
			r.ToolChoice = "required"
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			}
		}
	}
	return r, nil
}

// geminiText joins the text parts of a content, leaving out thoughts.
func geminiText(parts []gPart) string {
	var b strings.Builder
	for _, p := range parts {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// geminiEffort maps a Gemini thinking budget or level to an effort level.
// A budget of -1 means the model decides, which is medium here; 0 turns
// thinking off.
func geminiEffort(budget *int, level string) string {
	if budget != nil {
		n := *budget
		switch {
		case n < 0:
			return "medium"
		case n == 0:
			return ""
		case n <= 4096:
			return "low"
		case n <= 12000:
			return "medium"
		}
		return "high"
	}
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "MINIMAL", "LOW":
		return "low"
	case "MEDIUM":
		return "medium"
	case "HIGH":
		return "high"
	}
	return ""
}

// jsonInstruction asks for JSON in words, with the schema when there is one.
func jsonInstruction(schema json.RawMessage) string {
	s := "Reply only with a single valid JSON value: no prose, no markdown, no code fences."
	if sc := lowerSchema(schema); len(sc) > 0 {
		s += " The JSON must conform to this JSON Schema: " + string(sc)
	}
	return s
}

// geminiArgs is a function call's arguments, nil when there are none.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	if t := bytes.TrimSpace(raw); len(t) == 0 || string(t) == "null" {
		return nil
	}
	return raw
}

// geminiResult reads a function response into text. An "error" key marks
// the result as failed; an "output" string is used as is.
func geminiResult(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if e := m["error"]; e != nil {
			if s, ok := e.(string); ok {
				return s, true
			}
			b, _ := json.Marshal(e)
			return string(b), true
		}
		if out, ok := m["output"].(string); ok {
			return out, false
		}
	}
	return string(raw), false
}

// refID finds the id of the latest call with this name, or makes one.
func refID(refs []geminiCallRef, name string) string {
	for i := len(refs) - 1; i >= 0; i-- {
		if refs[i].name == name {
			return refs[i].id
		}
	}
	return geminiCallID()
}

// geminiInline keeps inline images and stands in text for other attachments.
func geminiInline(b *gBlob) Part {
	if strings.HasPrefix(b.MimeType, "image/") {
		return Part{Kind: Image, MediaType: b.MimeType, Data: b.Data}
	}
	return Part{Kind: Text, Text: "[attachment omitted: " + b.MimeType + "]"}
}

// geminiFile keeps image files that are reachable by URL.
func geminiFile(b *gBlob) Part {
	if strings.HasPrefix(b.MimeType, "image/") && strings.HasPrefix(b.FileURI, "http") {
		return Part{Kind: Image, MediaType: b.MimeType, URL: b.FileURI}
	}
	return Part{Kind: Text, Text: "[attachment omitted: " + b.FileURI + "]"}
}

// lowerSchema turns a schema in Gemini's dialect (OBJECT, STRING, …, with
// optional nullable and propertyOrdering) into a standard JSON Schema.
// A schema that does not parse is returned unchanged.
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
	out, err := json.Marshal(lowerValue(v))
	if err != nil {
		return raw
	}
	return out
}

func lowerValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			switch k {
			case "propertyOrdering":
				continue
			case "type":
				if s, ok := val.(string); ok {
					out[k] = strings.ToLower(s)
					continue
				}
			}
			out[k] = lowerValue(val)
		}
		if n, ok := out["nullable"].(bool); ok {
			delete(out, "nullable")
			if t, ok := out["type"].(string); ok && n {
				out["type"] = []any{t, "null"}
			}
		}
		return out
	case []any:
		for i := range x {
			x[i] = lowerValue(x[i])
		}
		return x
	}
	return v
}

// ---- reply --------------------------------------------------------------------

// stopToGemini is the finishReason for a stop reason.
func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// gemini is the usage block. promptTokenCount includes cached tokens, and
// thinking tokens are counted apart from candidatesTokenCount.
func (u Usage) gemini() map[string]any {
	prompt := u.Input + u.CacheRead
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	out := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand, "totalTokenCount": prompt + u.Output}
	if u.CacheRead > 0 {
		out["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		out["thoughtsTokenCount"] = u.Reasoning
	}
	return out
}

// geminiPart renders one part of a reply.
func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Thinking:
		return map[string]any{"text": p.Text, "thought": true}
	case ToolCall:
		id := p.ID
		if id == "" {
			id = geminiCallID()
		}
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}}
	}
	return map[string]any{"text": p.Text}
}

// renderGemini is the non-streaming reply. model is the name the client used.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		if (p.Kind == Text || p.Kind == Thinking) && p.Text == "" {
			continue
		}
		parts = append(parts, geminiPart(p))
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts},
		"finishReason": stopToGemini(res.Stop), "index": 0}
	b, _ := json.Marshal(map[string]any{"candidates": []any{cand}, "usageMetadata": res.Usage.gemini(),
		"modelVersion": model, "responseId": geminiRespID(res.ID)})
	return b
}

// geminiEncoder writes a reply as Gemini stream events. Each event carries
// the parts that became known; a function call is held until its arguments
// are whole and then sent in one piece. The last event has the finishReason
// and usage.
type geminiEncoder struct {
	w      *sseWriter
	model  string
	id     string
	tool   *Part // the open function call, nil for none
	args   strings.Builder
	tokens Usage
	stop   string
	failed bool
}

func (e *geminiEncoder) chunk(parts []map[string]any, finish string) {
	if e.id == "" {
		e.id = geminiRespID("")
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	ev := map[string]any{"candidates": []any{cand}, "modelVersion": e.model, "responseId": e.id}
	if finish != "" {
		cand["finishReason"] = finish
		ev["usageMetadata"] = e.tokens.gemini()
	}
	e.w.event("", ev)
}

// flushTool sends the open function call, if any, with its full arguments.
func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	p := *e.tool
	e.tool = nil
	args := parseArgs(e.args.String())
	e.args.Reset()
	e.chunk([]map[string]any{{"functionCall": map[string]any{"id": p.ID, "name": p.Name, "args": args}}}, "")
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
		e.tokens.add(ev.Usage)
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text}}, "")
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text, "thought": true}}, "")
		}
	case KToolStart:
		e.flushTool()
		id := ev.ID
		if id == "" {
			id = geminiCallID()
		}
		e.tool = &Part{Kind: ToolCall, ID: id, Name: ev.Name}
		e.args.Reset()
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.stop = ev.Stop
	case KUsage:
		e.tokens.add(ev.Usage)
	case KError:
		e.failed = true
		e.w.event("", map[string]any{"error": map[string]any{"code": 500, "message": ev.Text, "status": "INTERNAL"}})
	}
}

func (e *geminiEncoder) finish() {
	if e.failed {
		return
	}
	e.flushTool()
	e.chunk([]map[string]any{}, stopToGemini(e.stop))
}
