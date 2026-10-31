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

// ---- Google Gemini API (client side only) -----------------------------------

var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

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
	case 502, 503:
		return "UNAVAILABLE"
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	if code >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}

func geminiModelObject(e provider.Entry) map[string]any {
	return map[string]any{"name": "models/" + e.ID, "baseModelId": e.ID, "version": "dial", "displayName": e.Name,
		"description": e.Name + " via dial", "supportedGenerationMethods": geminiMethods}
}

func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, geminiModelObject(e))
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

func (s *Server) geminiModel(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.PathValue("rest"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, provider.Gemini, 404, "unknown model "+id)
}

// geminiPost serves /models/{model}:{method}.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, provider.Gemini, 404, "dial expects /v1beta/models/{model}:generateContent, :streamGenerateContent or :countTokens")
		return
	}
	model, method := strings.TrimPrefix(rest[:i], "models/"), rest[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, provider.Gemini, 400, err.Error())
		return
	}
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if len(bytes.TrimSpace(body)) == 0 {
		m = map[string]json.RawMessage{}
	} else if err := dec.Decode(&m); err != nil || m == nil {
		writeError(w, provider.Gemini, 400, "invalid request body")
		return
	}
	switch method {
	case "generateContent", "streamGenerateContent":
		stream := method == "streamGenerateContent"
		m["model"], _ = json.Marshal(model)
		m["_dial_stream"], _ = json.Marshal(stream)
		out, _ := json.Marshal(m)
		s.serve(w, r, provider.Gemini, out)
	case "countTokens":
		if inner, ok := m["generateContentRequest"]; ok {
			var im map[string]json.RawMessage
			if json.Unmarshal(inner, &im) == nil && im != nil {
				m = im
			}
		}
		out, _ := json.Marshal(m)
		req, err := parseGemini(out)
		if err != nil {
			writeError(w, provider.Gemini, 400, err.Error())
			return
		}
		n := len(req.System)
		for _, msg := range req.Messages {
			for _, p := range msg.Parts {
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
	default:
		writeError(w, provider.Gemini, 404, fmt.Sprintf("unsupported method %q; dial serves generateContent, streamGenerateContent and countTokens", method))
	}
}

type gPart struct {
	Text             *string `json:"text"`
	Thought          bool    `json:"thought"`
	ThoughtSignature string  `json:"thoughtSignature"`
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

// gContentOf reads a content that may also be given as a bare string.
func gContentOf(raw json.RawMessage) gContent {
	var c gContent
	if len(raw) == 0 {
		return c
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t := s
		c.Parts = []gPart{{Text: &t}}
		return c
	}
	json.Unmarshal(raw, &c)
	return c
}

type gRequest struct {
	Model             string            `json:"model"`
	Stream            bool              `json:"_dial_stream"`
	Contents          []json.RawMessage `json:"contents"`
	SystemInstruction json.RawMessage   `json:"systemInstruction"`
	Tools             []struct {
		FunctionDeclarations []struct {
			Name                 string          `json:"name"`
			Description          string          `json:"description"`
			Parameters           json.RawMessage `json:"parameters"`
			ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
		} `json:"functionDeclarations"`
	} `json:"tools"`
	ToolConfig struct {
		FunctionCallingConfig struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig struct {
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

// geminiSchema rewrites a Gemini schema (upper-case type names) into a
// standard JSON Schema.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	b, _ := json.Marshal(lowerSchema(v))
	return b
}

func lowerSchema(v any) any {
	switch x := v.(type) {
	case []any:
		for i := range x {
			x[i] = lowerSchema(x[i])
		}
		return x
	case map[string]any:
		for k, val := range x {
			switch k {
			case "type":
				switch t := val.(type) {
				case string:
					x[k] = strings.ToLower(t)
				case []any:
					for i := range t {
						if s, ok := t[i].(string); ok {
							t[i] = strings.ToLower(s)
						}
					}
				}
			case "properties", "$defs", "definitions", "patternProperties":
				if props, ok := val.(map[string]any); ok {
					for n, sub := range props {
						props[n] = lowerSchema(sub)
					}
				}
			case "enum", "required", "default", "example", "examples", "const":
			default:
				x[k] = lowerSchema(val)
			}
		}
		return x
	}
	return v
}

type gCall struct {
	name, id string
	turn     int
	used     bool
}

func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	gc := g.GenerationConfig
	r := &Request{Model: g.Model, Stream: g.Stream, MaxTokens: gc.MaxOutputTokens, Temp: gc.Temperature,
		TopP: gc.TopP, Stop: gc.StopSequences}

	// system
	var sys []string
	for _, p := range gContentOf(g.SystemInstruction).Parts {
		if p.Text != nil && *p.Text != "" {
			sys = append(sys, *p.Text)
		}
	}
	r.System = strings.Join(sys, "\n")

	// thinking
	if tc := gc.ThinkingConfig; tc != nil {
		r.Thinking = tc.IncludeThoughts
		switch {
		case tc.ThinkingLevel != "":
			r.Effort = effortOf(tc.ThinkingLevel)
		case tc.ThinkingBudget != nil:
			b := *tc.ThinkingBudget
			switch {
			case b < 0:
				r.Effort = "medium"
			case b == 0:
				r.Effort = ""
			default:
				r.Effort = effortOfBudget(b)
			}
		}
		if r.Effort != "" {
			r.Thinking = true
		}
		if tc.ThinkingBudget != nil && *tc.ThinkingBudget == 0 && tc.ThinkingLevel == "" {
			r.Thinking = false
		}
	}

	// contents
	var calls []*gCall
	findCall := func(name string) *gCall {
		var last *gCall
		for i := len(calls) - 1; i >= 0; i-- {
			if calls[i].name == name {
				last = calls[i]
				break
			}
		}
		if last == nil {
			return nil
		}
		for _, c := range calls {
			if c.name == name && c.turn == last.turn && !c.used {
				return c
			}
		}
		return last
	}
	seq := 0
	for turn, raw := range g.Contents {
		c := gContentOf(raw)
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		msg := Message{Role: role}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				id := p.FunctionCall.ID
				if id == "" {
					seq++
					id = fmt.Sprintf("call_%s_%d", newID(), seq)
				}
				calls = append(calls, &gCall{name: p.FunctionCall.Name, id: id, turn: turn})
				args := p.FunctionCall.Args
				if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
					args = json.RawMessage("{}")
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: args})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					if c := findCall(fr.Name); c != nil {
						id, c.used = c.id, true
					} else {
						seq++
						id = fmt.Sprintf("call_%s_%d", newID(), seq)
					}
				} else {
					for _, c := range calls {
						if c.id == id {
							c.used = true
						}
					}
				}
				out, isErr := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: isErr})
			case p.InlineData != nil:
				if strings.HasPrefix(strings.ToLower(p.InlineData.MimeType), "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment omitted: %s, %d bytes of base64 data]", p.InlineData.MimeType, len(p.InlineData.Data))})
				}
			case p.FileData != nil:
				if strings.HasPrefix(strings.ToLower(p.FileData.MimeType), "image/") && p.FileData.FileURI != "" && role == "user" {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment omitted: %s %s]", p.FileData.MimeType, p.FileData.FileURI)})
				}
			case p.Text != nil && p.Thought:
				if role == "assistant" && *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: *p.Text, Signature: p.ThoughtSignature})
				}
			case p.Text != nil:
				if *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
				}
			}
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}

	// tools
	fcc := g.ToolConfig.FunctionCallingConfig
	allowed := map[string]bool{}
	for _, n := range fcc.AllowedFunctionNames {
		allowed[n] = true
	}
	for _, t := range g.Tools {
		for _, d := range t.FunctionDeclarations {
			if d.Name == "" {
				continue
			}
			if len(allowed) > 0 && !allowed[d.Name] && strings.ToUpper(fcc.Mode) == "ANY" {
				continue
			}
			schema := geminiSchema(d.ParametersJSONSchema)
			if schema == nil {
				schema = geminiSchema(d.Parameters)
			}
			if schema == nil {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			r.Tools = append(r.Tools, Tool{Name: d.Name, Description: d.Description, Schema: schema})
		}
	}
	if len(r.Tools) > 0 {
		switch strings.ToUpper(fcc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			if len(r.Tools) == 1 {
				r.ToolChoice = "name:" + r.Tools[0].Name
			} else {
				r.ToolChoice = "required"
			}
		case "AUTO":
			r.ToolChoice = "auto"
		}
	}

	// structured output
	if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") || len(gc.ResponseSchema) > 0 || len(gc.ResponseJSONSchema) > 0 {
		r.JSON = true
		schema := geminiSchema(gc.ResponseJSONSchema)
		if schema == nil {
			schema = geminiSchema(gc.ResponseSchema)
		}
		note := "Respond with a single valid JSON value and nothing else: no prose, no markdown, no code fences."
		if schema != nil {
			note += " The JSON must conform to this JSON Schema:\n" + string(schema)
		}
		if r.System != "" {
			r.System += "\n\n"
		}
		r.System += note
	}
	return r, nil
}

// geminiResult turns a functionResponse's response object into the text a
// tool result carries.
func geminiResult(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		_, hasErr := m["error"]
		if len(m) == 1 {
			for _, k := range []string{"output", "result", "error"} {
				if v, ok := m[k]; ok {
					var s string
					if json.Unmarshal(v, &s) == nil {
						return s, hasErr
					}
				}
			}
		}
		return string(raw), hasErr
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	return string(raw), false
}

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
	prompt := u.Input + u.CacheRead
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	m := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand, "totalTokenCount": prompt + u.Output}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	return m
}

func geminiPartOf(p Part) map[string]any {
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
			id = "call_" + newID()
		}
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}}
	}
	return map[string]any{"text": p.Text}
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		if (p.Kind == Text || p.Kind == Thinking) && p.Text == "" {
			continue
		}
		parts = append(parts, geminiPartOf(p))
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	if res.Model != "" {
		model = res.Model
	}
	b, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{"content": map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(res.Stop), "index": 0}},
		"usageMetadata": res.Usage.gemini(), "modelVersion": model, "responseId": id})
	return b
}

// geminiEncoder writes events as a Gemini SSE stream. Function calls are
// held back until their arguments are complete.
type geminiEncoder struct {
	w       *sseWriter
	model   string
	col     collector
	pending *Part
	args    strings.Builder
	id      string
}

func (e *geminiEncoder) chunk(parts []map[string]any, finish string, u *Usage) {
	if e.id == "" {
		e.id = e.col.res.ID
		if e.id == "" {
			e.id = newID()
		}
	}
	model := e.model
	if e.col.res.Model != "" {
		model = e.col.res.Model
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	out := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": model, "responseId": e.id}
	if finish != "" {
		cand["finishReason"] = finish
	}
	if u != nil {
		out["usageMetadata"] = u.gemini()
	}
	e.w.event("", out)
}

func (e *geminiEncoder) flushTool() {
	if e.pending == nil {
		return
	}
	p := *e.pending
	p.Args = parseArgs(e.args.String())
	e.pending = nil
	e.args.Reset()
	e.chunk([]map[string]any{geminiPartOf(p)}, "", nil)
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KToolStart:
		e.flushTool()
		e.pending = &Part{Kind: ToolCall, ID: ev.ID, Name: ev.Name}
		if ev.ID == "" {
			e.pending.ID = "call_" + newID()
		}
	case KToolArgs:
		if e.pending != nil {
			e.args.WriteString(ev.Text)
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
	case KSig:
		e.flushTool()
		if ev.Text != "" {
			e.chunk([]map[string]any{{"text": "", "thought": true, "thoughtSignature": ev.Text}}, "", nil)
		}
	case KStop:
		e.flushTool()
	case KError:
		e.flushTool()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": geminiStatus(502)}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	res := e.col.finish()
	u := res.Usage
	e.chunk([]map[string]any{{"text": ""}}, geminiFinish(res.Stop), &u)
}
