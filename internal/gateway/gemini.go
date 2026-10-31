package gateway

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

// geminiProto is the Gemini API. It is a client protocol only: dial never
// speaks it to an upstream.
const geminiProto provider.Protocol = "gemini"

// ---- request ------------------------------------------------------------------

type gPart struct {
	Text             string `json:"text,omitempty"`
	Thought          bool   `json:"thought,omitempty"`
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
	InlineData       *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData,omitempty"`
	FileData *struct {
		MimeType string `json:"mimeType"`
		FileURI  string `json:"fileUri"`
	} `json:"fileData,omitempty"`
	FunctionCall *struct {
		ID   string          `json:"id"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall,omitempty"`
	FunctionResponse *struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse,omitempty"`
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
			IncludeThoughts bool   `json:"includeThoughts"`
			ThinkingBudget  *int   `json:"thinkingBudget"`
			ThinkingLevel   string `json:"thinkingLevel"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

// lowerTypes rewrites Gemini's upper-case schema types (OBJECT, STRING…)
// to the lower-case JSON Schema ones.
func lowerTypes(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			for k, e := range t {
				if k == "type" {
					switch s := e.(type) {
					case string:
						t[k] = strings.ToLower(s)
						continue
					case []any:
						for i, it := range s {
							if str, ok := it.(string); ok {
								s[i] = strings.ToLower(str)
							}
						}
						continue
					}
				}
				t[k] = walk(e)
			}
			return t
		case []any:
			for i, e := range t {
				t[i] = walk(e)
			}
			return t
		}
		return x
	}
	b, err := json.Marshal(walk(v))
	if err != nil {
		return raw
	}
	return b
}

// parseGemini reads a generateContent body. The model and whether the
// client streams come from the URL, not the body.
func parseGemini(body []byte, model string, stream bool) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: model, Stream: stream}
	if g.SystemInstruction != nil {
		var ts []string
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != "" {
				ts = append(ts, p.Text)
			}
		}
		r.System = strings.Join(ts, "\n")
	}
	// ids of calls made so far, by function name, newest last
	byName := map[string][]string{}
	used := map[string]bool{}
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
					id = fmt.Sprintf("call_%s%d", newID(), len(byName)+len(used))
					for used[id] {
						id += "x"
					}
				}
				used[id] = true
				byName[p.FunctionCall.Name] = append(byName[p.FunctionCall.Name], id)
				args := p.FunctionCall.Args
				if len(args) == 0 || string(args) == "null" {
					args = json.RawMessage("{}")
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: args})
			case p.FunctionResponse != nil:
				id := p.FunctionResponse.ID
				if id == "" {
					if ids := byName[p.FunctionResponse.Name]; len(ids) > 0 {
						id = ids[len(ids)-1]
					} else {
						id = "call_" + newID()
					}
				}
				out := string(p.FunctionResponse.Response)
				if out == "" || out == "null" {
					out = "{}"
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out})
				msg.Role = "user"
			case p.InlineData != nil:
				mt := p.InlineData.MimeType
				if strings.HasPrefix(mt, "image/") && msg.Role == "user" {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment: %s, %d bytes base64]", mt, len(p.InlineData.Data))})
				}
			case p.FileData != nil:
				mt := p.FileData.MimeType
				if (mt == "" || strings.HasPrefix(mt, "image/")) && msg.Role == "user" {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: mt, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file: %s %s]", mt, p.FileData.FileURI)})
				}
			case p.Thought:
				if p.Text != "" && msg.Role == "assistant" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text})
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
			schema := f.ParametersJSONSchema
			if len(schema) == 0 {
				schema = f.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: lowerTypes(schema)})
		}
	}
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO":
			r.ToolChoice = "auto"
		case "ANY", "VALIDATED":
			r.ToolChoice = "required"
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			}
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if th := gc.ThinkingConfig; th != nil {
			switch {
			case th.ThinkingBudget != nil && *th.ThinkingBudget < 0:
				r.Effort = "medium"
			case th.ThinkingBudget != nil && *th.ThinkingBudget > 0:
				r.Effort = effortOfBudget(*th.ThinkingBudget)
			case th.ThinkingLevel != "":
				r.Effort = effortOf(th.ThinkingLevel)
			}
			if r.Effort == "" && th.IncludeThoughts && (th.ThinkingBudget == nil || *th.ThinkingBudget != 0) {
				r.Effort = "medium"
			}
			r.Thinking = r.Effort != ""
		}
		if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			ins := "Respond with only valid JSON, with no prose and no code fences."
			if s := lowerTypes(schema); len(s) > 0 {
				ins += " The JSON must conform to this JSON schema: " + string(s)
			}
			if r.System != "" {
				r.System += "\n\n"
			}
			r.System += ins
		}
	}
	return r, nil
}

// ---- response -----------------------------------------------------------------

func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

func geminiUsage(u Usage) map[string]any {
	m := map[string]any{"promptTokenCount": u.Input, "candidatesTokenCount": u.Output,
		"totalTokenCount": u.Input + u.Output}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Thinking:
		return map[string]any{"text": p.Text, "thought": true}
	case ToolCall:
		return map[string]any{"functionCall": map[string]any{"id": p.ID, "name": p.Name, "args": argsOf(p)}}
	}
	return map[string]any{"text": p.Text}
}

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
	b, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(res.Stop), "index": 0}},
		"usageMetadata": geminiUsage(res.Usage), "modelVersion": model, "responseId": id})
	return b
}

// geminiEncoder writes events as a Gemini SSE stream, one part per event.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector
	tool  *Part
	args  strings.Builder
	done  bool
}

func (e *geminiEncoder) chunk(parts []map[string]any, finish string, u *Usage) {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	out := map[string]any{"candidates": []any{cand}, "modelVersion": e.model, "responseId": e.id}
	if finish != "" {
		cand["finishReason"] = finish
		out["usageMetadata"] = geminiUsage(*u)
	}
	e.w.event("", out)
}

func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	p := *e.tool
	s := strings.TrimSpace(e.args.String())
	if s == "" {
		s = "{}"
	}
	p.Args = json.RawMessage(s)
	if !json.Valid(p.Args) {
		p.Args = parseArgs(s)
	}
	if p.ID == "" {
		p.ID = "call_" + newID()
	}
	e.tool = nil
	e.args.Reset()
	e.chunk([]map[string]any{geminiPart(p)}, "", nil)
}

func (e *geminiEncoder) event(ev Event) {
	if e.id == "" {
		e.id = newID()
	}
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
		if ev.Model != "" {
			e.model = ev.Model
		}
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text}}, "", nil)
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": ev.Text, "thought": true}}, "", nil)
		}
	case KToolStart:
		e.flushTool()
		e.tool = &Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
	case KToolArgs:
		e.args.WriteString(ev.Text)
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	if e.done {
		return
	}
	e.done = true
	if e.id == "" {
		e.id = newID()
	}
	e.flushTool()
	res := e.col.finish()
	fin := geminiFinish(res.Stop)
	if e.col.err != "" && len(res.Parts) == 0 {
		fin = "OTHER"
	}
	e.chunk([]map[string]any{}, fin, &res.Usage)
}

// ---- errors, routes -------------------------------------------------------------

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
	case 501:
		return "UNIMPLEMENTED"
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

func geminiError(status int, msg string) any {
	if status == 529 {
		status = 503
	}
	return map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}}
}

// splitModel separates "{model}:{method}" at the last colon.
func splitModel(rest string) (model, method string) {
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return rest, ""
	}
	return rest[:i], rest[i+1:]
}

func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

func geminiModelObject(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.Model
	}
	return map[string]any{"name": "models/" + e.ID, "displayName": name, "description": name + " via dial",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}

func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("rest")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, geminiProto, 404, "unknown model "+id)
}

// geminiBody unwraps a countTokens body that carries generateContentRequest.
func geminiBody(body []byte) []byte {
	var w struct {
		Req json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &w) == nil && len(w.Req) > 0 {
		return w.Req
	}
	return body
}

func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	model, method := splitModel(r.PathValue("rest"))
	model = strings.TrimPrefix(model, "models/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	start := time.Now()
	call := Call{Time: start, From: geminiProto, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	var stream bool
	switch method {
	case "generateContent":
	case "streamGenerateContent":
		stream = true
	case "countTokens":
	default:
		writeError(w, geminiProto, 404, fmt.Sprintf("unsupported method %q; dial serves generateContent, streamGenerateContent and countTokens under /v1beta/models/", method))
		return
	}
	p, up, ok := provider.Resolve(model)
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
	if method == "countTokens" {
		req, err := parseGemini(geminiBody(body), model, false)
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
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"totalTokens":%d}`, n/4)
		return
	}
	call.Provider = p.ID
	req, err := parseGemini(body, model, stream)
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	to := p.Speaks()
	if len(to) == 0 {
		writeError(w, geminiProto, 502, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.translateReq(w, r, p, geminiProto, to[0], up, req, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: up,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}
