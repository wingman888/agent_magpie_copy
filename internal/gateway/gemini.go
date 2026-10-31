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

// ---- Gemini API (client side only) -------------------------------------------
//
// Upstreams never speak Gemini; the gateway serves it to clients such as
// Gemini CLI and always translates.

type gInline struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gPart struct {
	Text             string   `json:"text,omitempty"`
	Thought          bool     `json:"thought,omitempty"`
	ThoughtSignature string   `json:"thoughtSignature,omitempty"`
	InlineData       *gInline `json:"inlineData,omitempty"`
	FileData         *struct {
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
	Model             string     `json:"model"`
	Stream            bool       `json:"stream"`
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
			ThinkingBudget  *int   `json:"thinkingBudget"`
			ThinkingLevel   string `json:"thinkingLevel"`
			IncludeThoughts bool   `json:"includeThoughts"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

// lowerSchema turns a Gemini schema (types in capitals) into standard JSON
// Schema, recursively.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			for k, e := range x {
				if k == "type" {
					switch t := e.(type) {
					case string:
						x[k] = strings.ToLower(t)
						continue
					case []any:
						for i, s := range t {
							if str, ok := s.(string); ok {
								t[i] = strings.ToLower(str)
							}
						}
						continue
					}
				}
				x[k] = walk(e)
			}
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
		}
		return v
	}
	out, err := json.Marshal(walk(v))
	if err != nil {
		return raw
	}
	return out
}

func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}
	if si := g.SystemInstruction; si != nil {
		var b strings.Builder
		for _, p := range si.Parts {
			b.WriteString(p.Text)
		}
		r.System = b.String()
	}
	type call struct {
		id, name string
		used     bool
	}
	var calls []*call
	seq := 0
	newCallID := func() string { seq++; return fmt.Sprintf("call_%s_%d", newID(), seq) }
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
					id = newCallID()
				}
				calls = append(calls, &call{id: id, name: p.FunctionCall.Name})
				args := p.FunctionCall.Args
				if len(args) == 0 || string(bytes.TrimSpace(args)) == "null" {
					args = json.RawMessage("{}")
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: args})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					for i := len(calls) - 1; i >= 0 && id == ""; i-- {
						if calls[i].name == fr.Name && !calls[i].used {
							id = calls[i].id
						}
					}
					for i := len(calls) - 1; i >= 0 && id == ""; i-- {
						if calls[i].name == fr.Name {
							id = calls[i].id
						}
					}
					if id == "" {
						id = newCallID()
					}
				}
				for _, c := range calls {
					if c.id == id {
						c.used = true
					}
				}
				out := string(fr.Response)
				if len(fr.Response) == 0 || out == "null" {
					out = "{}"
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Name: fr.Name, Text: out})
			case p.InlineData != nil:
				if strings.HasPrefix(strings.ToLower(p.InlineData.MimeType), "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment: %s, not supported]", p.InlineData.MimeType)})
				}
			case p.FileData != nil:
				if strings.HasPrefix(strings.ToLower(p.FileData.MimeType), "image/") && p.FileData.FileURI != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment: %s, not supported]", p.FileData.MimeType)})
				}
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
			if len(bytes.TrimSpace(schema)) == 0 {
				schema = d.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: d.Name, Description: d.Description, Schema: lowerSchema(schema)})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO":
			r.ToolChoice = "auto"
		case "ANY":
			r.ToolChoice = "required"
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else if len(fc.AllowedFunctionNames) > 1 {
				allowed := map[string]bool{}
				for _, n := range fc.AllowedFunctionNames {
					allowed[n] = true
				}
				var keep []Tool
				for _, t := range r.Tools {
					if allowed[t.Name] {
						keep = append(keep, t)
					}
				}
				if len(keep) > 0 {
					r.Tools = keep
				}
			}
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		if th := gc.ThinkingConfig; th != nil {
			if th.IncludeThoughts {
				r.Thinking = true
			}
			if th.ThinkingBudget != nil {
				b := *th.ThinkingBudget
				switch {
				case b < 0:
					r.Effort = "medium"
				case b > 0:
					r.Effort = effortOfBudget(b)
				}
			}
			if e := effortOf(th.ThinkingLevel); e != "" {
				r.Effort = e
			}
			if r.Effort != "" {
				r.Thinking = true
			}
		}
		schema := gc.ResponseJSONSchema
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = gc.ResponseSchema
		}
		schema = lowerSchema(schema)
		if strings.EqualFold(gc.ResponseMimeType, "application/json") || len(schema) > 0 {
			inst := "Respond with valid JSON only: no prose, no markdown code fences."
			if len(schema) > 0 {
				inst += " The JSON must conform to this JSON Schema:\n" + string(schema)
			}
			if r.System != "" {
				r.System += "\n\n"
			}
			r.System += inst
		}
	}
	return r, nil
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
	case 409:
		return "ABORTED"
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

func finishToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// usage in Gemini terms: the prompt count includes cache hits, and
// thinking tokens are counted apart from the candidates'.
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

func geminiCall(id, name string, args json.RawMessage) map[string]any {
	if id == "" {
		id = "call_" + newID()
	}
	return map[string]any{"functionCall": map[string]any{"id": id, "name": name, "args": args}}
}

func geminiChunk(parts []map[string]any, finish string, usage *Usage, id, model string) map[string]any {
	if parts == nil {
		parts = []map[string]any{}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	out := map[string]any{"candidates": []any{cand}, "modelVersion": model, "responseId": id}
	if usage != nil {
		out["usageMetadata"] = usage.gemini()
	}
	return out
}

// geminiEncoder writes events as a Gemini SSE stream. Text and thoughts go
// out as they come; a function call goes out whole, once its arguments are
// complete.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector

	tool     bool
	toolID   string
	toolName string
	toolArgs strings.Builder
	sentTool bool
	failed   bool
}

func (e *geminiEncoder) send(parts []map[string]any, finish string, u *Usage) {
	if e.id == "" {
		e.id = "resp_" + newID()
	}
	e.w.event("", geminiChunk(parts, finish, u, e.id, e.model))
}

func (e *geminiEncoder) flushTool() {
	if !e.tool {
		return
	}
	e.tool = false
	s := strings.TrimSpace(e.toolArgs.String())
	e.toolArgs.Reset()
	if s == "" {
		s = "{}"
	}
	if !json.Valid([]byte(s)) || !strings.HasPrefix(s, "{") {
		return // truncated arguments are never sent
	}
	e.sentTool = true
	e.send([]map[string]any{geminiCall(e.toolID, e.toolName, json.RawMessage(s))}, "", nil)
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
		e.flushTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text}}, "", nil)
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.send([]map[string]any{{"text": ev.Text, "thought": true}}, "", nil)
		}
	case KToolStart:
		e.flushTool()
		e.tool, e.toolID, e.toolName = true, ev.ID, ev.Name
	case KToolArgs:
		if e.tool {
			e.toolArgs.WriteString(ev.Text)
		}
	case KStop:
		e.flushTool()
	case KError:
		e.flushTool()
		e.failed = true
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	if e.failed {
		return
	}
	res := e.col.finish()
	u := res.Usage
	e.send(nil, finishToGemini(res.Stop), &u)
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
			if args := argsOf(p); json.Valid(args) {
				parts = append(parts, geminiCall(p.ID, p.Name, args))
			}
		}
	}
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = "resp_" + newID()
	}
	u := res.Usage
	b, _ := json.Marshal(geminiChunk(parts, finishToGemini(res.Stop), &u, id, model))
	return b
}

// ---- HTTP -----------------------------------------------------------------------

// geminiTarget splits "fake/m1:streamGenerateContent" at the last colon.
func geminiTarget(rest string) (model, method string) {
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, ""
}

func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	model, method := geminiTarget(r.PathValue("model"))
	model = strings.TrimPrefix(model, "models/")
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	switch method {
	case "generateContent", "streamGenerateContent":
	case "countTokens":
		s.geminiCount(w, body)
		return
	default:
		writeError(w, provider.Gemini, 404, fmt.Sprintf("unsupported method %q; dial serves generateContent, streamGenerateContent and countTokens", method))
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	m := map[string]any{}
	if err := dec.Decode(&m); err != nil {
		writeError(w, provider.Gemini, 400, "invalid request: "+err.Error())
		return
	}
	m["model"] = model
	m["stream"] = method == "streamGenerateContent"
	body, _ = json.Marshal(m)
	s.serve(w, r, provider.Gemini, model, body)
}

func (s *Server) geminiCount(w http.ResponseWriter, body []byte) {
	var wrap struct {
		Req json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Req) > 0 {
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
			if p.Kind == Image {
				n += 1000
			}
		}
	}
	for _, t := range req.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	total := n / 4
	if n > 0 && total == 0 {
		total = 1
	}
	writeJSON(w, 200, map[string]any{"totalTokens": total})
}

func geminiModelObject(e provider.Entry) map[string]any {
	return map[string]any{"name": "models/" + e.ID, "displayName": e.Name, "description": e.Name + " via dial",
		"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"}}
}

func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("model"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, "unknown model "+id)
}
