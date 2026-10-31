package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// ---- Gemini API (client side only) --------------------------------------------
//
// Gemini is never spoken to an upstream; it is only what a client such as
// Gemini CLI sends. Every Gemini call is therefore translated.

// gemini is the client protocol tag of the Gemini API.
const gemini provider.Protocol = "gemini"

type gPart struct {
	Text             string `json:"text"`
	Thought          bool   `json:"thought"`
	ThoughtSignature string `json:"thoughtSignature"`
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

type gRequest struct {
	Model             string     `json:"model"`
	Stream            bool       `json:"__stream"`
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

var geminiIDs int

func geminiCallID() string {
	geminiIDs++
	return fmt.Sprintf("call_%s%d", newID(), geminiIDs)
}

// fixSchema turns a Gemini (OpenAPI flavoured) schema into standard JSON
// Schema: type names to lower case, Gemini-only keys dropped.
func fixSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch k {
			case "enum", "const", "default", "example", "examples":
				continue
			case "properties", "$defs", "definitions", "patternProperties":
				if m, ok := val.(map[string]any); ok {
					for pk, pv := range m {
						m[pk] = fixSchema(pv)
					}
					continue
				}
			case "type":
				if s, ok := val.(string); ok {
					x[k] = strings.ToLower(s)
					continue
				}
				if arr, ok := val.([]any); ok {
					for i := range arr {
						if s, ok := arr[i].(string); ok {
							arr[i] = strings.ToLower(s)
						}
					}
					continue
				}
			case "nullable", "propertyOrdering":
				delete(x, k)
				continue
			case "minItems", "maxItems", "minLength", "maxLength", "minProperties", "maxProperties":
				if s, ok := val.(string); ok {
					if n, err := strconv.ParseInt(s, 10, 64); err == nil {
						x[k] = n
						continue
					}
				}
			}
			x[k] = fixSchema(val)
		}
	case []any:
		for i := range x {
			x[i] = fixSchema(x[i])
		}
	}
	return v
}

func cleanSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	b, err := json.Marshal(fixSchema(v))
	if err != nil {
		return raw
	}
	return b
}

func geminiEffort(budget int) string {
	switch {
	case budget <= 4096:
		return "low"
	case budget <= 16384:
		return "medium"
	}
	return "high"
}

func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}
	if g.SystemInstruction != nil {
		var sys []string
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != "" {
				sys = append(sys, p.Text)
			}
		}
		r.System = strings.Join(sys, "\n\n")
	}

	type call struct {
		id, name string
		used     bool
	}
	var calls []*call
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
					id = geminiCallID()
				}
				calls = append(calls, &call{id: id, name: p.FunctionCall.Name})
				args := p.FunctionCall.Args
				if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
					args = json.RawMessage("{}")
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: args})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					var hit *call
					for i := len(calls) - 1; i >= 0; i-- {
						if calls[i].name == fr.Name && !calls[i].used {
							hit = calls[i]
							break
						}
					}
					if hit == nil {
						for i := len(calls) - 1; i >= 0; i-- {
							if calls[i].name == fr.Name {
								hit = calls[i]
								break
							}
						}
					}
					if hit != nil {
						id = hit.id
					} else {
						id = geminiCallID()
					}
				}
				for _, c := range calls {
					if c.id == id {
						c.used = true
					}
				}
				out, isErr := functionOutput(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: isErr})
			case p.InlineData != nil:
				if strings.HasPrefix(strings.ToLower(p.InlineData.MimeType), "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment omitted: " + p.InlineData.MimeType + "]"})
				}
			case p.FileData != nil:
				if strings.HasPrefix(strings.ToLower(p.FileData.MimeType), "image/") && p.FileData.FileURI != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attachment omitted: " + p.FileData.MimeType + " " + p.FileData.FileURI + "]"})
				}
			case p.Thought:
				if n := len(msg.Parts); n > 0 && msg.Parts[n-1].Kind == Thinking && p.Text == "" {
					msg.Parts[n-1].Signature += p.ThoughtSignature
				} else if n > 0 && msg.Parts[n-1].Kind == Thinking && msg.Parts[n-1].Signature == "" {
					msg.Parts[n-1].Text += p.Text
					msg.Parts[n-1].Signature = p.ThoughtSignature
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
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
		for _, fd := range t.FunctionDeclarations {
			schema := fd.ParametersJSONSchema
			if len(bytes.TrimSpace(schema)) == 0 {
				schema = fd.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: cleanSchema(schema)})
		}
	}
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY", "VALIDATED":
			switch len(fc.AllowedFunctionNames) {
			case 0:
				r.ToolChoice = "required"
			case 1:
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			default:
				r.ToolChoice = "required"
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
		case "AUTO":
			r.ToolChoice = "auto"
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens, r.Temp, r.TopP, r.Stop = gc.MaxOutputTokens, gc.Temperature, gc.TopP, gc.StopSequences
		schema := gc.ResponseJSONSchema
		if len(bytes.TrimSpace(schema)) == 0 {
			schema = gc.ResponseSchema
		}
		schema = cleanSchema(schema)
		if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") || len(schema) > 0 {
			r.JSON, r.JSONSchema = true, schema
		}
		if tc := gc.ThinkingConfig; tc != nil {
			if tc.IncludeThoughts {
				r.Thinking = true
			}
			switch {
			case tc.ThinkingBudget != nil:
				switch b := *tc.ThinkingBudget; {
				case b < 0:
					r.Effort, r.Thinking = "medium", true
				case b == 0:
					r.Effort, r.Thinking = "", false
				default:
					r.Effort, r.Thinking = geminiEffort(b), true
				}
			case tc.ThinkingLevel != "":
				if e := effortOf(tc.ThinkingLevel); e != "" {
					r.Effort, r.Thinking = e, true
				}
			}
		}
	}
	return r, nil
}

// functionOutput flattens a functionResponse's response object.
func functionOutput(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil && len(m) == 1 {
		for _, k := range []string{"output", "error"} {
			if v, ok := m[k]; ok {
				var s string
				if json.Unmarshal(v, &s) == nil {
					return s, k == "error"
				}
			}
		}
	}
	return string(raw), false
}

// ---- reply side ------------------------------------------------------------------

func stopToGemini(s string) string {
	switch s {
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
	m := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": cand, "totalTokenCount": prompt + cand + u.Reasoning}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	return m
}

// callArgs is a tool call's arguments if they form a complete JSON object.
func callArgs(p Part) (json.RawMessage, bool) {
	s := strings.TrimSpace(string(p.Args))
	if s == "" || s == "null" {
		return json.RawMessage("{}"), true
	}
	if strings.HasPrefix(s, "{") && json.Valid([]byte(s)) {
		return json.RawMessage(s), true
	}
	return nil, false
}

func geminiCall(id, name string, args json.RawMessage) map[string]any {
	if id == "" {
		id = geminiCallID()
	}
	return map[string]any{"functionCall": map[string]any{"id": id, "name": name, "args": args}}
}

func geminiThought(text, sig string) map[string]any {
	m := map[string]any{"text": text, "thought": true}
	if sig != "" {
		m["thoughtSignature"] = sig
	}
	return m
}

func geminiResponse(parts []map[string]any, finish string, u *Usage, model, id string) map[string]any {
	cand := map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}}
	if finish != "" {
		cand["finishReason"] = finish
	}
	out := map[string]any{"candidates": []any{cand}, "modelVersion": model, "responseId": id}
	if u != nil {
		out["usageMetadata"] = u.gemini()
	}
	return out
}

// geminiEncoder writes events as Gemini response chunks. A function call is
// held back until its arguments are complete and then sent whole.
type geminiEncoder struct {
	w       *sseWriter
	model   string
	id      string
	started bool
	tool    *Part
	args    strings.Builder
	col     collector
}

func (e *geminiEncoder) start(ev Event) {
	if e.started {
		return
	}
	e.started = true
	e.id = ev.MsgID
	if e.id == "" {
		e.id = newID()
	}
	if ev.Model != "" {
		e.model = ev.Model
	}
}

func (e *geminiEncoder) emit(parts ...map[string]any) {
	e.w.event("", geminiResponse(parts, "", nil, e.model, e.id))
}

func (e *geminiEncoder) flushTool() {
	if e.tool == nil {
		return
	}
	p := *e.tool
	p.Args = json.RawMessage(strings.TrimSpace(e.args.String()))
	e.tool = nil
	e.args.Reset()
	if args, ok := callArgs(p); ok {
		e.emit(geminiCall(p.ID, p.Name, args))
	}
}

func (e *geminiEncoder) event(ev Event) {
	if ev.Kind != KStart && !e.started {
		e.start(Event{})
	}
	if ev.Kind != KToolArgs && ev.Kind != KUsage && ev.Kind != KStart {
		e.flushTool()
	}
	switch ev.Kind {
	case KStart:
		e.start(ev)
	case KText:
		if ev.Text != "" {
			e.emit(map[string]any{"text": ev.Text})
		}
	case KThink:
		if ev.Text != "" {
			e.emit(geminiThought(ev.Text, ""))
		}
	case KSig:
		if ev.Text != "" {
			e.emit(geminiThought("", ev.Text))
		}
	case KToolStart:
		id := ev.ID
		if id == "" {
			id = geminiCallID()
		}
		e.tool = &Part{Kind: ToolCall, ID: id, Name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.args.WriteString(ev.Text)
		}
	case KError:
		e.w.event("", geminiErrorBody(502, ev.Text))
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	if !e.started {
		e.start(Event{})
	}
	e.flushTool()
	res := e.col.finish()
	e.w.event("", geminiResponse([]map[string]any{{"text": ""}}, stopToGemini(res.Stop), &res.Usage, e.model, e.id))
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
			parts = append(parts, geminiThought(p.Text, p.Signature))
		case ToolCall:
			if args, ok := callArgs(p); ok {
				parts = append(parts, geminiCall(p.ID, p.Name, args))
			}
		}
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"text": ""})
	}
	if res.Model != "" {
		model = res.Model
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(geminiResponse(parts, stopToGemini(res.Stop), &res.Usage, model, id))
	return b
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
	return "INVALID_ARGUMENT"
}

func geminiErrorBody(code int, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg, "status": geminiStatus(code)}}
}

// ---- HTTP -------------------------------------------------------------------------

func geminiModelObject(e provider.Entry) map[string]any {
	return map[string]any{"name": "models/" + e.ID, "version": "001", "displayName": e.Name, "description": e.Name + " via dial",
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
	id := strings.TrimPrefix(r.PathValue("rest"), "models/")
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeJSON(w, 200, geminiModelObject(e))
			return
		}
	}
	writeError(w, gemini, 404, "unknown model "+id)
}

// geminiPost serves /v1beta/models/{model}:{method}.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		writeError(w, gemini, 404, "dial serves /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	model, method := strings.TrimPrefix(rest[:i], "models/"), rest[i+1:]
	switch method {
	case "generateContent", "streamGenerateContent":
		stream := method == "streamGenerateContent"
		s.serve(w, r, gemini, func(body []byte) ([]byte, error) {
			dec := json.NewDecoder(bytes.NewReader(body))
			dec.UseNumber()
			var m map[string]any
			if err := dec.Decode(&m); err != nil || m == nil {
				return nil, fmt.Errorf("invalid JSON payload")
			}
			m["model"], m["__stream"] = model, stream
			return json.Marshal(m)
		})
	case "countTokens":
		s.geminiCount(w, r)
	default:
		writeError(w, gemini, 404, "unknown method "+method+"; dial serves generateContent, streamGenerateContent and countTokens")
	}
}

func (s *Server) geminiCount(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, gemini, 400, err.Error())
		return
	}
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.GenerateContentRequest) > 0 {
		body = wrap.GenerateContentRequest
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, gemini, 400, err.Error())
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
	if total == 0 && n > 0 {
		total = 1
	}
	writeJSON(w, 200, map[string]any{"totalTokens": total})
}
