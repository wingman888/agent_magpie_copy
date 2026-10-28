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

// ---- Gemini API -------------------------------------------------------------
//
// Gemini clients (Gemini CLI) only ever talk to the gateway in this API; the
// gateway never speaks it to a provider, so there is a parser and an encoder
// here but no builder or decoder.

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
	SystemInstruction      json.RawMessage `json:"systemInstruction"`
	SystemInstructionSnake json.RawMessage `json:"system_instruction"`
	Contents               []gContent      `json:"contents"`
	Tools                  []struct {
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
		ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"`
		ResponseSchema     json.RawMessage `json:"responseSchema"`
		ThinkingConfig     *struct {
			ThinkingLevel  string `json:"thinkingLevel"`
			ThinkingBudget *int   `json:"thinkingBudget"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

var gCounter atomic.Uint64

// gID makes an id for a tool call the client or upstream left unnamed.
func gID() string {
	return fmt.Sprintf("call_%x%d", time.Now().UnixNano(), gCounter.Add(1))
}

// geminiSystem reads systemInstruction: a Content, a parts array or a string.
func geminiSystem(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []gPart
	var c gContent
	if json.Unmarshal(raw, &c) == nil && c.Parts != nil {
		parts = c.Parts
	} else if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var texts []string
	for _, p := range parts {
		if p.Thought || p.Text == "" {
			continue
		}
		texts = append(texts, p.Text)
	}
	return strings.Join(texts, "\n\n")
}

// geminiSchema turns a Gemini schema into plain lowercase JSON Schema.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	b, err := json.Marshal(convSchema(v))
	if err != nil {
		return raw
	}
	return b
}

func convSchema(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = convSchema(e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			switch k {
			case "propertyOrdering", "example":
				continue
			case "nullable":
				if b, ok := e.(bool); ok && b {
					out["x-nullable"] = true
				}
				continue
			case "type":
				switch t := e.(type) {
				case string:
					out[k] = strings.ToLower(t)
				case []any:
					ts := make([]any, len(t))
					for i, s := range t {
						if str, ok := s.(string); ok {
							ts[i] = strings.ToLower(str)
						} else {
							ts[i] = s
						}
					}
					out[k] = ts
				default:
					out[k] = e
				}
			case "properties", "$defs", "definitions":
				if m, ok := e.(map[string]any); ok {
					pm := map[string]any{}
					for name, sub := range m {
						pm[name] = convSchema(sub)
					}
					out[k] = pm
				} else {
					out[k] = convSchema(e)
				}
			default:
				out[k] = convSchema(e)
			}
		}
		return out
	}
	return v
}

const geminiJSONOnly = "Reply with only a single JSON value and nothing else: no prose, no code fences."

func parseGemini(body []byte, model string, stream bool) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	sys := g.SystemInstruction
	if len(sys) == 0 {
		sys = g.SystemInstructionSnake
	}
	r := &Request{Model: model, System: geminiSystem(sys), Stream: stream}

	type seen struct{ id, name string }
	var calls []seen
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" || c.Role == "assistant" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					id = gID()
				}
				args := p.FunctionCall.Args
				if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
					args = json.RawMessage("{}")
				}
				calls = append(calls, seen{id, p.FunctionCall.Name})
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: args})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					for i := len(calls) - 1; i >= 0; i-- {
						if calls[i].name == fr.Name {
							id = calls[i].id
							break
						}
					}
				}
				out, isErr := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: isErr})
			case p.InlineData != nil:
				if strings.HasPrefix(p.InlineData.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment " + p.InlineData.MimeType + "]"})
				}
			case p.FileData != nil:
				msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
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
		for _, d := range t.FunctionDeclarations {
			schema := d.ParametersJSONSchema
			if len(bytes.TrimSpace(schema)) == 0 || string(bytes.TrimSpace(schema)) == "null" {
				schema = d.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: d.Name, Description: d.Description, Schema: geminiSchema(schema)})
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
		default:
			r.ToolChoice = "auto"
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if th := gc.ThinkingConfig; th != nil {
			if e := effortOf(th.ThinkingLevel); e != "" {
				r.Effort = e
			} else if th.ThinkingBudget != nil {
				if *th.ThinkingBudget < 0 {
					r.Effort = "medium"
				} else {
					r.Effort = effortOfBudget(*th.ThinkingBudget)
				}
			}
			r.Thinking = r.Effort != ""
		}
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			instr := geminiJSONOnly
			schema := gc.ResponseJSONSchema
			if len(bytes.TrimSpace(schema)) == 0 || string(bytes.TrimSpace(schema)) == "null" {
				schema = gc.ResponseSchema
			}
			if s := geminiSchema(schema); len(s) > 0 {
				instr += " It must match this JSON schema: " + string(s)
			}
			if r.System != "" {
				r.System += "\n\n"
			}
			r.System += instr
		}
	}
	return r, nil
}

// geminiResult is the text of a functionResponse's response object.
func geminiResult(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && len(m) == 1 {
		for _, k := range []string{"output", "error"} {
			if v, ok := m[k]; ok {
				var s string
				if json.Unmarshal(v, &s) != nil {
					s = string(v)
				}
				return s, k == "error"
			}
		}
	}
	return string(raw), false
}

// ---- replies ------------------------------------------------------------------

func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

func (u Usage) gemini() map[string]any {
	out := map[string]any{
		"promptTokenCount":     u.Input + u.CacheRead,
		"candidatesTokenCount": max(0, u.Output-u.Reasoning),
		"totalTokenCount":      u.Input + u.CacheRead + u.Output,
	}
	if u.CacheRead > 0 {
		out["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		out["thoughtsTokenCount"] = u.Reasoning
	}
	return out
}

func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Thinking:
		m := map[string]any{"text": p.Text, "thought": true}
		if p.Signature != "" {
			m["thoughtSignature"] = p.Signature
		}
		return m
	case ToolCall:
		id := p.ID
		if id == "" {
			id = gID()
		}
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}}
	}
	return map[string]any{"text": p.Text}
}

func geminiEnvelope(parts []map[string]any, finish string, u *Usage, model, id string) map[string]any {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	out := map[string]any{"candidates": []any{cand}, "modelVersion": model, "responseId": id}
	if u != nil {
		out["usageMetadata"] = u.gemini()
	}
	return out
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		if p.Kind == Text || p.Kind == Thinking || p.Kind == ToolCall {
			parts = append(parts, geminiPart(p))
		}
	}
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(geminiEnvelope(parts, geminiFinish(res.Stop), &res.Usage, model, id))
	return b
}

// geminiEncoder writes events as a Gemini SSE stream. Tool calls are held
// back until their arguments are whole.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector
	tool  *Part // the call being assembled
	args  strings.Builder
}

func (e *geminiEncoder) send(parts []map[string]any, finish string, u *Usage) {
	if e.id == "" {
		e.id = newID()
	}
	e.w.event("", geminiEnvelope(parts, finish, u, e.model, e.id))
}

func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	p := *e.tool
	p.Args = parseArgs(e.args.String())
	e.tool = nil
	e.args.Reset()
	e.send([]map[string]any{geminiPart(p)}, "", nil)
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
		if ev.Text != "" {
			e.flushTool()
			e.send([]map[string]any{{"text": ev.Text}}, "", nil)
		}
	case KThink:
		if ev.Text != "" {
			e.flushTool()
			e.send([]map[string]any{{"text": ev.Text, "thought": true}}, "", nil)
		}
	case KSig:
		if ev.Text != "" {
			e.flushTool()
			e.send([]map[string]any{{"text": "", "thought": true, "thoughtSignature": ev.Text}}, "", nil)
		}
	case KToolStart:
		e.flushTool()
		id := ev.ID
		if id == "" {
			id = gID()
		}
		e.tool = &Part{Kind: ToolCall, ID: id, Name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KError:
		e.flushTool()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	res := e.col.finish()
	e.send([]map[string]any{}, geminiFinish(res.Stop), &res.Usage)
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
	case 500:
		return "INTERNAL"
	case 502, 503, 529:
		return "UNAVAILABLE"
	}
	return "UNKNOWN"
}

// ---- routes -------------------------------------------------------------------

// geminiModels lists the catalog the way the Gemini API does.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, map[string]any{"name": "models/" + e.ID, "displayName": e.Name,
			"description":                e.Name + " via dial",
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// gemini serves /v1beta/models/{model}:{method}.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	i := strings.LastIndex(path, ":")
	if i < 0 {
		writeError(w, provider.Gemini, 404, "no method in path "+path)
		return
	}
	id, method := path[:i], path[i+1:]
	id = strings.TrimPrefix(id, "models/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	switch method {
	case "countTokens":
		var wrap struct {
			Req json.RawMessage `json:"generateContentRequest"`
		}
		if json.Unmarshal(body, &wrap) == nil && len(wrap.Req) > 0 {
			body = wrap.Req
		}
		req, err := parseGemini(body, id, false)
		if err != nil {
			writeError(w, provider.Gemini, 400, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"totalTokens": estimateTokens(req)})
	case "generateContent", "streamGenerateContent":
		s.geminiGenerate(w, r, id, body, method == "streamGenerateContent")
	default:
		writeError(w, provider.Gemini, 404, "unknown method "+method)
	}
}

func (s *Server) geminiGenerate(w http.ResponseWriter, r *http.Request, id string, body []byte, stream bool) {
	start := time.Now()
	from := provider.Gemini
	call := Call{Time: start, From: from, Model: id, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, model, ok := provider.Resolve(id)
	if !ok {
		call.Status, call.Error = 404, "unknown model"
		s.record(call)
		writeError(w, from, 404, unknownModelMsg(id))
		return
	}
	req, err := parseGemini(body, id, stream)
	if err != nil {
		writeError(w, from, 400, err.Error())
		return
	}
	call.Provider = p.ID
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, from, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.translateReq(w, r, p, from, to[0], model, req, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: model,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}
