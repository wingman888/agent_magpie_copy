package gateway

// The Gemini API, served as a client protocol only: a Gemini client talks to
// dial, and dial reaches the provider in whichever of its own protocols it
// speaks. Requests are read into the same Request the other APIs use, and
// replies are written back in Gemini's shape.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/provider"
	"github.com/yetone/dial/internal/usage"
)

// clientGemini names the Gemini API in the call log. It is never a provider
// protocol.
const clientGemini provider.Protocol = "gemini"

// geminiMethods are the methods dial serves for every model.
func geminiMethods() []string {
	return []string{"generateContent", "streamGenerateContent", "countTokens"}
}

// gemini routes POST /v1beta/models/{model}:{method}. The model reference
// may itself contain slashes and colons, so the method is whatever follows
// the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	ref, verb := rest, ""
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		ref, verb = rest[:i], rest[i+1:]
	}
	ref = strings.TrimPrefix(ref, "models/")
	switch verb {
	case "generateContent", "streamGenerateContent", "countTokens":
	default:
		writeGemini(w, 404, fmt.Sprintf("%q is not a method dial serves; use generateContent, streamGenerateContent or countTokens under /v1beta/models/{model}", rest))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGemini(w, 400, err.Error())
		return
	}
	if verb == "countTokens" {
		s.geminiCount(w, ref, body)
		return
	}
	s.geminiGenerate(w, r, ref, body, verb == "streamGenerateContent")
}

// geminiModels is GET /v1beta/models: the catalog, under Gemini's names.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		name := e.Name
		if name == "" {
			name = e.ID
		}
		models = append(models, map[string]any{"name": "models/" + e.ID, "version": "dial",
			"displayName": name, "supportedGenerationMethods": geminiMethods()})
	}
	writeGeminiJSON(w, 200, map[string]any{"models": models})
}

func unknownGeminiModel(ref string) string {
	return fmt.Sprintf("models/%s is not found; dial serves the models listed by GET /v1beta/models", ref)
}

// geminiCount estimates the tokens of a request, as the other APIs' count
// endpoints do. The body is either a generate request or one wrapped in
// generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, ref string, body []byte) {
	if _, _, ok := provider.Resolve(ref); !ok {
		writeGemini(w, 404, unknownGeminiModel(ref))
		return
	}
	raw := body
	var wrap struct {
		Req json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Req) > 0 {
		raw = wrap.Req
	}
	req, err := parseGemini(raw)
	if err != nil {
		writeGemini(w, 400, err.Error())
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
	writeGeminiJSON(w, 200, map[string]int{"totalTokens": n / 4})
}

// geminiGenerate serves generateContent and streamGenerateContent.
func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, ref string, body []byte, stream bool) {
	start := time.Now()
	call := Call{Time: start, From: clientGemini, Model: ref, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(ref)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeGemini(w, 404, unknownGeminiModel(ref))
		return
	}
	call.Provider = p.ID
	req, err := parseGemini(body)
	if err != nil {
		call.Status, call.Error = 400, err.Error()
		s.record(call)
		writeGemini(w, 400, err.Error())
		return
	}
	req.Model = model
	to := p.Speaks()
	if len(to) == 0 {
		writeGemini(w, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.geminiUpstream(w, r, p, to[0], req, stream, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// geminiUpstream sends the request to the provider in protocol to, always
// streamed, and answers the Gemini client from the events that come back.
func (s *Server) geminiUpstream(w http.ResponseWriter, r *http.Request, p provider.Provider, to provider.Protocol, req *Request, stream bool, u *Usage) (int, string) {
	req.Stream = true
	res, err := s.forward(r.Context(), p, to, pathOf(to), build(to, req, req.Model, p.Host()), r.Header)
	if err != nil {
		return writeGemini(w, 502, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeGemini(w, res.StatusCode, msg), msg
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeGemini(w, 502, msg), msg
	}
	dec := decoder(to)
	if stream {
		enc := &geminiStream{w: newSSEWriter(w), id: newID(), model: req.Model}
		readSSE(res.Body, func(_, data string) error {
			return dec(data, func(ev Event) {
				if ev.Kind == KStart || ev.Kind == KUsage {
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		if enc.err != "" && !enc.w.begun {
			return writeGemini(w, 502, p.Name+": "+enc.err), enc.err
		}
		enc.finish()
		return 200, enc.err
	}
	var col collector
	readSSE(res.Body, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeGemini(w, 502, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(renderGemini(res2, req.Model))
	return 200, col.err
}

// ---- Gemini request → Request ---------------------------------------------------

type gRequest struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Tools             []gTool    `json:"tools"`
	ToolConfig        *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *gGenConfig `json:"generationConfig"`
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
		IncludeThoughts bool   `json:"includeThoughts"`
		ThinkingLevel   string `json:"thinkingLevel"`
	} `json:"thinkingConfig"`
}

// parseGemini reads a generateContent body into the common request.
func parseGemini(body []byte) (*Request, error) {
	var q gRequest
	if err := json.Unmarshal(body, &q); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}

	var sys []string
	if q.SystemInstruction != nil {
		for _, p := range q.SystemInstruction.Parts {
			if p.Text != "" && !p.Thought {
				sys = append(sys, p.Text)
			}
		}
	}
	r.System = strings.Join(sys, "\n\n")

	// the id of the latest call to each function, for responses sent
	// without one
	latest := map[string]string{}
	for _, c := range q.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		msg := Message{Role: role}
		flush := func() {
			if len(msg.Parts) > 0 {
				r.Messages = append(r.Messages, msg)
			}
			msg = Message{Role: role}
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = "call_" + newID()
				}
				latest[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: parseArgs(string(fc.Args))})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = latest[fr.Name]
				}
				if id == "" {
					id = "call_" + newID()
				}
				text, isErr := geminiResponseText(fr.Response)
				flush()
				r.Messages = append(r.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: id, Text: text, IsError: isErr}}})
			case p.Thought:
				if p.Text != "" || p.ThoughtSignature != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
				}
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			case p.InlineData != nil:
				b := p.InlineData
				if strings.HasPrefix(b.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: b.MimeType, Data: b.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment of type %s, not forwarded]", b.MimeType)})
				}
			case p.FileData != nil:
				f := p.FileData
				if strings.HasPrefix(f.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: f.MimeType, URL: f.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file %s (%s)]", f.FileURI, f.MimeType)})
				}
			}
		}
		flush()
	}

	for _, t := range q.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.Parameters
			if len(schema) == 0 {
				schema = fd.ParametersJSONSchema
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: geminiSchema(schema)})
		}
	}
	if q.ToolConfig != nil && q.ToolConfig.FunctionCallingConfig != nil {
		fc := q.ToolConfig.FunctionCallingConfig
		switch fc.Mode {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		case "ANY":
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		}
	}

	if gc := q.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			effort := ""
			if tc.ThinkingBudget != nil {
				switch b := *tc.ThinkingBudget; {
				case b < 0:
					effort = "medium" // the model decides; treat as medium
				case b == 0:
					effort = ""
				default:
					effort = effortOfBudget(b)
				}
			}
			if e := effortOf(tc.ThinkingLevel); e != "" {
				effort = e
			}
			if effort == "" && tc.IncludeThoughts {
				effort = "medium"
			}
			r.Effort = effort
			r.Thinking = effort != ""
		}
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			instr := "Respond only with a single valid JSON value, with no prose or code fences."
			if len(schema) > 0 {
				instr += " The JSON must conform to this JSON schema: " + string(geminiSchema(schema))
			}
			if r.System != "" {
				r.System += "\n\n"
			}
			r.System += instr
		}
	}
	return r, nil
}

// geminiResponseText reads a function response. An "output" or "error"
// string is the text; anything else is passed on as JSON.
func geminiResponseText(raw json.RawMessage) (string, bool) {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if s, ok := m["error"].(string); ok && s != "" {
			return s, true
		}
		if s, ok := m["output"].(string); ok {
			return s, false
		}
	}
	return string(raw), false
}

// geminiSchema turns a schema's upper-case type names (OBJECT, STRING…) into
// the lower-case ones JSON Schema and the other APIs use.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(lowerSchemaTypes(v))
	if err != nil {
		return raw
	}
	return out
}

func lowerSchemaTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "propertyOrdering")
		for k, val := range x {
			if s, ok := val.(string); ok && k == "type" {
				x[k] = strings.ToLower(s)
				continue
			}
			x[k] = lowerSchemaTypes(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = lowerSchemaTypes(x[i])
		}
		return x
	}
	return v
}

// ---- Request → Gemini reply -----------------------------------------------------

// geminiCall is a function call as Gemini shows it. Calls the upstream sent
// without an id get one, so the client can answer them.
func geminiCall(p Part) map[string]any {
	id := p.ID
	if id == "" {
		id = "call_" + newID()
	}
	return map[string]any{"id": id, "name": p.Name, "args": parseArgs(argsString(p))}
}

// geminiPart is one part of a reply; false for parts Gemini has no place for.
func geminiPart(p Part) (map[string]any, bool) {
	switch p.Kind {
	case Text:
		if p.Text == "" {
			return nil, false
		}
		return map[string]any{"text": p.Text}, true
	case Thinking:
		if p.Text == "" && p.Signature == "" {
			return nil, false
		}
		m := map[string]any{"text": p.Text, "thought": true}
		if p.Signature != "" {
			m["thoughtSignature"] = p.Signature
		}
		return m, true
	case ToolCall:
		return map[string]any{"functionCall": geminiCall(p)}, true
	}
	return nil, false
}

// stopToGemini maps a finish reason to Gemini's.
func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage is the usage metadata. Output counts reasoning tokens in dial,
// Gemini keeps them apart, so candidates excludes them.
func geminiUsage(u Usage) map[string]any {
	in := u.Input + u.CacheRead
	out := u.Output - u.Reasoning
	if out < 0 {
		out = 0
	}
	m := map[string]any{"promptTokenCount": in, "candidatesTokenCount": out,
		"thoughtsTokenCount": u.Reasoning, "totalTokenCount": in + u.Output}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

// renderGemini is the whole reply, for non-streaming clients.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		if part, ok := geminiPart(p); ok {
			parts = append(parts, part)
		}
	}
	name := model
	if res.Model != "" {
		name = res.Model
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": stopToGemini(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  name,
		"responseId":    id,
	})
	return b
}

// geminiStream turns upstream events into Gemini stream events: one event
// per part, in the order the parts arrived, then a final event with the
// finish reason and usage.
type geminiStream struct {
	w     *sseWriter
	id    string
	model string
	col   collector // assembles the arguments of the open tool call
	tool  bool      // a tool call is open
	usage Usage
	stop  string
	err   string
}

func (g *geminiStream) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			g.id = ev.MsgID
		}
		if ev.Model != "" {
			g.model = ev.Model
		}
		g.usage.add(ev.Usage)
	case KText:
		g.closeTool()
		if ev.Text != "" {
			g.emit(map[string]any{"text": ev.Text})
		}
	case KThink:
		g.closeTool()
		if ev.Text != "" {
			g.emit(map[string]any{"text": ev.Text, "thought": true})
		}
	case KSig:
		g.closeTool()
		if ev.Text != "" {
			g.emit(map[string]any{"text": "", "thought": true, "thoughtSignature": ev.Text})
		}
	case KToolStart:
		g.closeTool()
		g.col.add(ev)
		g.tool = true
	case KToolArgs:
		g.col.add(ev)
	case KStop:
		g.closeTool()
		g.stop = ev.Stop
	case KUsage:
		g.usage.add(ev.Usage)
	case KError:
		g.err = ev.Text
	}
}

// closeTool sends the open tool call, once its arguments are complete.
func (g *geminiStream) closeTool() {
	if !g.tool {
		return
	}
	g.tool = false
	g.col.closeTool()
	p := g.col.res.Parts[len(g.col.res.Parts)-1]
	g.emit(map[string]any{"functionCall": geminiCall(p)})
}

func (g *geminiStream) emit(part map[string]any) {
	g.w.event("", map[string]any{
		"candidates":   []map[string]any{{"content": map[string]any{"role": "model", "parts": []map[string]any{part}}, "index": 0}},
		"modelVersion": g.model,
		"responseId":   g.id,
	})
}

func (g *geminiStream) finish() {
	if g.err != "" {
		g.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": g.err, "status": "UNAVAILABLE"}})
		return
	}
	g.closeTool()
	g.w.begin()
	g.w.event("", map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": []any{}},
			"finishReason": stopToGemini(g.stop), "index": 0}},
		"usageMetadata": geminiUsage(g.usage),
		"modelVersion":  g.model,
		"responseId":    g.id,
	})
}

// ---- errors ----------------------------------------------------------------------

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
	return "INTERNAL"
}

// writeGemini answers with Gemini's error shape.
func writeGemini(w http.ResponseWriter, code int, msg string) int {
	writeGeminiJSON(w, code, map[string]any{"error": map[string]any{"code": code, "message": msg, "status": geminiStatus(code)}})
	return code
}

// writeGeminiJSON writes compact JSON, as Gemini does.
func writeGeminiJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}
