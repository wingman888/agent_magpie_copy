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

// Gemini is the Gemini API as a client protocol: Gemini CLI and other Gemini
// API clients talk it to the gateway, which runs their requests through the
// same resolve / translate / record pipeline as the other three APIs. No
// upstream vendor speaks it, so it only ever appears on the client side.
const geminiProto provider.Protocol = "gemini"

// geminiMethods are the generation methods every catalog model supports.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// ---- wire types -----------------------------------------------------------------

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
	Text             string     `json:"text,omitempty"`
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

type gFuncDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description,omitempty"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type gGenConfig struct {
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
}

type gRequest struct {
	Model             string     `json:"model,omitempty"`  // set by the gateway from the URL
	Stream            bool       `json:"stream,omitempty"` // set by the gateway from the method
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent `json:"systemInstruction,omitempty"`
	Tools             []struct {
		FunctionDeclarations []gFuncDecl `json:"functionDeclarations"`
	} `json:"tools,omitempty"`
	ToolConfig *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
		} `json:"functionCallingConfig,omitempty"`
	} `json:"toolConfig,omitempty"`
	GenerationConfig *gGenConfig `json:"generationConfig,omitempty"`
}

// ---- request side -----------------------------------------------------------------

var geminiSeq atomic.Uint64

// geminiID makes an id for a call or reply that arrived without one.
func geminiID() string { return fmt.Sprintf("%s%d", newID(), geminiSeq.Add(1)) }

// parseGemini turns a Gemini request into the gateway's request shape.
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
			b.WriteString(p.Text)
		}
		if s := b.String(); s != "" {
			sys = append(sys, s)
		}
	}
	// the latest call id per function name, for results that carry no id
	lastID := map[string]string{}
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.Thought:
				msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			case p.InlineData != nil:
				if strings.HasPrefix(p.InlineData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment of type %s omitted]", p.InlineData.MimeType)})
				}
			case p.FileData != nil:
				if strings.HasPrefix(p.FileData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file %s of type %s omitted]", p.FileData.FileURI, p.FileData.MimeType)})
				}
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + geminiID()
				}
				lastID[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: parseArgs(string(fc.Args))})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = lastID[fr.Name]
				}
				if id == "" {
					id = "call_" + geminiID()
				}
				text, isErr := responseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
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
				schema = lowerSchema(fd.Parameters)
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: schema})
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
			r.ToolChoice = "required"
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			}
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP, r.Stop = gc.Temperature, gc.TopP, gc.StopSequences
		if strings.EqualFold(gc.ResponseMimeType, "application/json") {
			sys = append(sys, jsonInstruction(gc))
		}
		if tc := gc.ThinkingConfig; tc != nil {
			r.Thinking = tc.IncludeThoughts
			if tc.ThinkingBudget != nil {
				switch b := *tc.ThinkingBudget; {
				case b < 0:
					r.Effort = "medium" // -1: the model decides, which is medium here
				case b == 0:
				default:
					r.Effort = effortOfBudget(b)
				}
			}
			if lv := effortOf(tc.ThinkingLevel); lv != "" {
				r.Effort = lv
			}
			if r.Effort != "" {
				r.Thinking = true
			}
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// jsonInstruction asks for JSON output in words, for upstreams without a
// native JSON mode the gateway can rely on.
func jsonInstruction(gc *gGenConfig) string {
	s := "Respond only with a single valid JSON value. Do not write any other text or code fences."
	schema := gc.ResponseJSONSchema
	if len(schema) == 0 {
		schema = lowerSchema(gc.ResponseSchema)
	}
	if len(schema) > 0 {
		s += " The JSON must conform to this JSON Schema: " + string(schema)
	}
	return s
}

// responseText reads a functionResponse's payload as text. Gemini clients
// put the output under "output" (or "content"); an "error" marks a failure.
func responseText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if s, ok := m["output"].(string); ok {
			return s, false
		}
		if s, ok := m["content"].(string); ok {
			return s, false
		}
		if e, ok := m["error"]; ok {
			if s, ok := e.(string); ok {
				return s, true
			}
			b, _ := json.Marshal(e)
			return string(b), true
		}
	}
	return string(raw), false
}

// lowerSchema turns Gemini's upper-case schema type names (OBJECT, STRING…)
// into standard lower-case JSON Schema, recursively.
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
	out, err := json.Marshal(lowerNode(v))
	if err != nil {
		return raw
	}
	return out
}

func lowerNode(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	for k, x := range m {
		switch k {
		case "type":
			if s, ok := x.(string); ok {
				m[k] = strings.ToLower(s)
			}
		case "properties":
			if props, ok := x.(map[string]any); ok {
				for pk, pv := range props {
					props[pk] = lowerNode(pv)
				}
			}
		case "items", "not", "additionalProperties":
			m[k] = lowerNode(x)
		case "anyOf", "oneOf", "allOf":
			if arr, ok := x.([]any); ok {
				for i := range arr {
					arr[i] = lowerNode(arr[i])
				}
			}
		}
	}
	return m
}

// geminiBody stamps the catalog model id and the stream flag onto a Gemini
// body, so the shared parser sees them where it expects them.
func geminiBody(body []byte, model string, stream bool) []byte {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil || m == nil {
		m = map[string]any{}
	}
	m["model"], m["stream"] = model, stream
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- response side -------------------------------------------------------------------

// stopToGemini maps the gateway's stop reasons to Gemini finish reasons.
func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage reports usage the Gemini way: the prompt count includes cache
// hits, thinking tokens are counted apart from the candidate tokens.
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

// geminiParts renders a reply's parts as Gemini parts.
func geminiParts(parts []Part) []map[string]any {
	out := []map[string]any{}
	for _, p := range parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				out = append(out, map[string]any{"text": p.Text})
			}
		case Thinking:
			m := map[string]any{"text": p.Text, "thought": true}
			if p.Signature != "" {
				m["thoughtSignature"] = p.Signature
			}
			out = append(out, m)
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + geminiID()
			}
			out = append(out, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": json.RawMessage(argsOf(p))}})
		}
	}
	return out
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "resp_" + geminiID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": geminiParts(res.Parts)},
			"finishReason": stopToGemini(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
	return b
}

// geminiEncoder writes events as a Gemini SSE stream. A function call is
// held back until its arguments are complete, then sent whole.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	stop  string
	usage Usage
	tool  *geminiToolCall
	args  strings.Builder
}

type geminiToolCall struct {
	id, name string
}

func (e *geminiEncoder) responseID() string {
	if e.id == "" {
		e.id = "resp_" + geminiID()
	}
	return e.id
}

func (e *geminiEncoder) send(parts []map[string]any, finish any) {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != nil {
		cand["finishReason"] = finish
	}
	e.w.event("", map[string]any{"candidates": []map[string]any{cand}, "modelVersion": e.model, "responseId": e.responseID()})
}

func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	id := e.tool.id
	if id == "" {
		id = "call_" + geminiID()
	}
	args := parseArgs(e.args.String())
	e.send([]map[string]any{{"functionCall": map[string]any{"id": id, "name": e.tool.name, "args": args}}}, nil)
	e.tool = nil
	e.args.Reset()
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
		e.flushTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text}}, nil)
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text, "thought": true}}, nil)
		}
	case KSig:
		if ev.Text != "" {
			e.send([]map[string]any{{"text": "", "thought": true, "thoughtSignature": ev.Text}}, nil)
		}
	case KToolStart:
		e.flushTool()
		e.tool = &geminiToolCall{id: ev.ID, name: ev.Name}
		e.args.Reset()
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushTool()
		e.stop = ev.Stop
	case KUsage:
		e.usage.add(ev.Usage)
	case KError:
		e.flushTool()
		e.w.event("", map[string]any{"error": gErr(500, ev.Text)})
	}
}

// finish sends the closing event, which carries the finish reason and usage.
func (e *geminiEncoder) finish() {
	e.flushTool()
	e.w.event("", map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": []any{}},
			"finishReason": stopToGemini(e.stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(e.usage),
		"modelVersion":  e.model,
		"responseId":    e.responseID(),
	})
}

// gErr is a Gemini-style error body.
func gErr(code int, msg string) map[string]any {
	return map[string]any{"code": code, "message": msg, "status": geminiStatus(code)}
}

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

// ---- HTTP -------------------------------------------------------------------------

// splitGeminiPath reads "{model}:{method}"; the method follows the last colon,
// since model ids may contain colons of their own.
func splitGeminiPath(rest string) (model, method string, ok bool) {
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// geminiModelObject describes a catalog model the Gemini way.
func geminiModelObject(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "version": "001", "displayName": name,
		"description": name + " via " + e.Provider.Name + " (dial)", "supportedGenerationMethods": geminiMethods}
}

// geminiModels lists every catalog model.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiModel describes one catalog model.
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

// geminiPost serves generateContent, streamGenerateContent and countTokens.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	model, method, ok := splitGeminiPath(r.PathValue("rest"))
	if !ok {
		writeError(w, geminiProto, 404, "dial serves /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	switch method {
	case "generateContent", "streamGenerateContent":
	case "countTokens":
		s.geminiCount(w, model, body)
		return
	default:
		writeError(w, geminiProto, 404, fmt.Sprintf("unknown method %q; dial serves generateContent, streamGenerateContent and countTokens", method))
		return
	}
	if !json.Valid(body) {
		writeError(w, geminiProto, 400, "invalid request: body is not JSON")
		return
	}
	start := time.Now()
	call := Call{Time: start, From: geminiProto, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, resolved, ok := provider.Resolve(model)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		msg := fmt.Sprintf("dial knows no model %q", model)
		if ids := provider.IDs(); len(ids) > 0 {
			msg += "; it has " + strings.Join(ids, ", ")
		} else {
			msg += "; add a provider in dial first"
		}
		writeError(w, geminiProto, 404, msg)
		return
	}
	call.Provider = p.ID
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, geminiProto, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	stream := method == "streamGenerateContent"
	call.Status, call.Error = s.translate(w, r, p, geminiProto, to[0], resolved, geminiBody(body, model, stream), &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: resolved,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiCount estimates tokens at roughly four characters each. The body is
// either the request itself or one wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, geminiProto, 404, fmt.Sprintf("dial knows no model %q", model))
		return
	}
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	json.Unmarshal(body, &wrap)
	src := body
	if len(wrap.GenerateContentRequest) > 0 {
		src = wrap.GenerateContentRequest
	}
	req, err := parseGemini(src)
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	n := len(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name) + len(p.Data)
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	writeJSON(w, 200, map[string]any{"totalTokens": n / 4})
}
