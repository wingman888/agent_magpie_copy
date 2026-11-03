package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/yetone/dial/internal/provider"
)

// The Gemini API is served as a client protocol: Gemini CLI and other Gemini
// clients call the gateway, which translates to whatever the vendor speaks.
// No vendor ever sees it, so provider.Base(gemini) is always "".
const gemini provider.Protocol = "gemini"

const geminiPrefix = "/v1beta/models/"

// ---- HTTP -------------------------------------------------------------------

// geminiModels lists every catalog model, under Gemini's "models/<id>" names.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		name := e.Name
		if name == "" {
			name = e.ID
		}
		models = append(models, map[string]any{
			"name":                       "models/" + e.ID,
			"version":                    "dial",
			"displayName":                name,
			"description":                e.Provider.Name + " via dial",
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent", "countTokens"},
		})
	}
	writeJSON(w, 200, map[string]any{"models": models})
}

// geminiPost routes POST /v1beta/models/{model}:{method}. Model ids may hold
// slashes and colons, so the method is whatever follows the last colon.
func (s *Server) geminiPost(w http.ResponseWriter, r *http.Request) {
	id, method, ok := splitGeminiPath(strings.TrimPrefix(r.URL.Path, geminiPrefix))
	if !ok {
		writeError(w, gemini, 404, "expected /v1beta/models/{model}:{method} in "+r.URL.Path)
		return
	}
	switch method {
	case "generateContent", "countTokens":
	case "streamGenerateContent":
		if r.URL.Query().Get("alt") != "sse" {
			writeError(w, gemini, 400, "streamGenerateContent is served only with alt=sse")
			return
		}
	default:
		writeError(w, gemini, 404, fmt.Sprintf("unknown method %q; dial serves generateContent, streamGenerateContent and countTokens", method))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, gemini, 400, err.Error())
		return
	}
	if method == "countTokens" {
		s.geminiCount(w, id, body)
		return
	}
	s.serve(w, r, gemini, body, id, method == "streamGenerateContent")
}

// splitGeminiPath cuts "fake/m1:generateContent" into model and method.
func splitGeminiPath(rest string) (model, method string, ok bool) {
	i := strings.LastIndex(rest, ":")
	if i <= 0 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// geminiCount estimates tokens from characters, about four per token. The
// body may be the request itself or wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, id string, body []byte) {
	if _, _, ok := provider.Resolve(id); !ok {
		writeError(w, gemini, 404, unknownModel(id))
		return
	}
	inner := body
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && rawOrNil(wrap.GenerateContentRequest) != nil {
		inner = wrap.GenerateContentRequest
	}
	req, err := parseGemini(inner)
	if err != nil {
		writeError(w, gemini, 400, err.Error())
		return
	}
	n := utf8.RuneCountInString(req.System)
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			n += utf8.RuneCountInString(p.Text) + utf8.RuneCountInString(p.Name) + utf8.RuneCount(p.Args)
		}
	}
	for _, t := range req.Tools {
		n += utf8.RuneCountInString(t.Name) + utf8.RuneCountInString(t.Description) + utf8.RuneCount(t.Schema)
	}
	tokens := (n + 3) / 4
	if tokens < 1 {
		tokens = 1
	}
	writeJSON(w, 200, map[string]any{"totalTokens": tokens})
}

// ---- request ----------------------------------------------------------------

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFile struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
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
	FileData         *gFile   `json:"fileData"`
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

type gTool struct {
	FunctionDeclarations []gFuncDecl `json:"functionDeclarations"`
}

type gToolConfig struct {
	FunctionCallingConfig *struct {
		Mode                 string   `json:"mode"`
		AllowedFunctionNames []string `json:"allowedFunctionNames"`
	} `json:"functionCallingConfig"`
}

type gThinking struct {
	ThinkingBudget  *int   `json:"thinkingBudget"`
	ThinkingLevel   string `json:"thinkingLevel"`
	IncludeThoughts bool   `json:"includeThoughts"`
}

type gGeneration struct {
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
	Contents          []gContent   `json:"contents"`
	SystemInstruction *gContent    `json:"systemInstruction"`
	Tools             []gTool      `json:"tools"`
	ToolConfig        *gToolConfig `json:"toolConfig"`
	GenerationConfig  *gGeneration `json:"generationConfig"`
}

// parseGemini reads a generateContent request into the common form.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	if g.SystemInstruction != nil {
		r.System = geminiText(g.SystemInstruction.Parts)
	}
	var calls []Part // every call so far, so results without ids can find theirs
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
					id = "call_" + newID()
				}
				args := json.RawMessage("{}")
				if a := rawOrNil(fc.Args); a != nil {
					args = parseArgs(string(a))
				}
				calls = append(calls, Part{ID: id, Name: fc.Name})
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: args})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = latestCall(calls, fr.Name)
				}
				if id == "" {
					id = "call_" + newID()
				}
				text, failed := geminiResult(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: failed})
			case p.InlineData != nil:
				msg.Parts = append(msg.Parts, geminiMedia(p.InlineData.MimeType, p.InlineData.Data, ""))
			case p.FileData != nil:
				msg.Parts = append(msg.Parts, geminiMedia(p.FileData.MimeType, "", p.FileData.FileURI))
			case p.Thought:
				if p.Text != "" || p.ThoughtSignature != "" {
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
			schema := rawOrNil(fd.ParametersJSONSchema)
			if schema == nil {
				schema = rawOrNil(fd.Parameters)
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: normalizeSchema(schema)})
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
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if th := gc.ThinkingConfig; th != nil {
			switch {
			case th.ThinkingBudget != nil && *th.ThinkingBudget == 0:
				// thinking off
			case th.ThinkingBudget != nil && *th.ThinkingBudget < 0:
				r.Effort = "medium" // the model decides; medium is the middle ground
			case th.ThinkingBudget != nil:
				r.Effort = effortOfBudget(*th.ThinkingBudget)
			case th.ThinkingLevel != "":
				r.Effort = effortOf(th.ThinkingLevel)
			}
			r.Thinking = th.IncludeThoughts || r.Effort != ""
		}
		// Structured output: the common form has no JSON mode, so every
		// vendor gets the same instruction to answer with JSON only.
		schema := rawOrNil(gc.ResponseJSONSchema)
		if schema == nil {
			schema = rawOrNil(gc.ResponseSchema)
		}
		if gc.ResponseMimeType == "application/json" || schema != nil {
			note := "Reply with only a JSON value: no prose, no code fences."
			if schema != nil {
				note += " The JSON must follow this JSON schema: " + string(normalizeSchema(schema))
			}
			if r.System != "" {
				r.System += "\n\n" + note
			} else {
				r.System = note
			}
		}
	}
	return r, nil
}

// geminiText joins the visible text of a list of parts.
func geminiText(parts []gPart) string {
	var b strings.Builder
	for _, p := range parts {
		if !p.Thought {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// geminiMedia keeps images and stands in text for other attachments.
func geminiMedia(mime, data, uri string) Part {
	if strings.HasPrefix(mime, "image/") {
		return Part{Kind: Image, MediaType: mime, Data: data, URL: uri}
	}
	return Part{Kind: Text, Text: "[attachment: " + mime + "]"}
}

// latestCall finds the id of the most recent call with the given name.
func latestCall(calls []Part, name string) string {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Name == name {
			return calls[i].ID
		}
	}
	return ""
}

// geminiResult reads a function response: {"output":…} is the result,
// {"error":…} a failure, anything else is passed on as JSON.
func geminiResult(raw json.RawMessage) (string, bool) {
	raw = rawOrNil(raw)
	if raw == nil {
		return "", false
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if e, ok := m["error"]; ok && e != nil {
			if s, ok := e.(string); ok {
				return s, true
			}
			j, _ := json.Marshal(e)
			return string(j), true
		}
		if s, ok := m["output"].(string); ok {
			return s, false
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	return string(raw), false
}

// rawOrNil returns the raw JSON, or nil when it is absent or null.
func rawOrNil(raw json.RawMessage) json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return nil
	}
	return json.RawMessage(t)
}

// normalizeSchema turns Gemini's upper-case type names (OBJECT, STRING…)
// into standard lower-case JSON Schema types.
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	raw = rawOrNil(raw)
	if raw == nil {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return raw
	}
	out, err := json.Marshal(lowerTypes(v))
	if err != nil {
		return raw
	}
	return out
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
					for i := range t {
						if s, ok := t[i].(string); ok {
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

// ---- response ---------------------------------------------------------------

// geminiFinish maps the common stop reason to Gemini's finishReason.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage counts as Gemini does: the prompt includes cache reads, and
// thinking tokens are reported apart from the candidate tokens.
func geminiUsage(u Usage) map[string]any {
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

// geminiBody is one response object. Usage and finishReason ride only on
// the last piece of a reply.
func geminiBody(parts []map[string]any, finish string, u Usage, model, id string) map[string]any {
	if parts == nil {
		parts = []map[string]any{}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	out := map[string]any{"candidates": []map[string]any{cand}, "modelVersion": model, "responseId": id}
	if finish != "" {
		cand["finishReason"] = finish
		out["usageMetadata"] = geminiUsage(u)
	}
	return out
}

// geminiPart is one reply part, or nil when there is nothing to say.
func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Text:
		if p.Text != "" {
			return map[string]any{"text": p.Text}
		}
	case Thinking:
		if p.Text != "" {
			m := map[string]any{"text": p.Text, "thought": true}
			if p.Signature != "" {
				m["thoughtSignature"] = p.Signature
			}
			return m
		}
	case ToolCall:
		id := p.ID
		if id == "" {
			id = "call_" + newID()
		}
		return map[string]any{"functionCall": map[string]any{"name": p.Name, "args": parseArgs(argsString(p)), "id": id}}
	}
	return nil
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	var parts []map[string]any
	for _, p := range res.Parts {
		if m := geminiPart(p); m != nil {
			parts = append(parts, m)
		}
	}
	id := res.ID
	if id == "" {
		id = newID()
	}
	b, _ := json.Marshal(geminiBody(parts, geminiFinish(res.Stop), res.Usage, model, id))
	return b
}

// geminiEncoder writes a Gemini SSE stream. A function call is held until it
// is complete, then sent whole, so no client ever sees half an argument list.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	col   collector
	tool  *geminiTool
}

type geminiTool struct {
	id, name string
	args     strings.Builder
}

func (e *geminiEncoder) rid() string {
	if e.id == "" {
		e.id = newID()
	}
	return e.id
}

// send writes one part as a chunk of its own.
func (e *geminiEncoder) send(p map[string]any) {
	if p == nil {
		return
	}
	e.w.event("", geminiBody([]map[string]any{p}, "", Usage{}, e.model, e.rid()))
}

func (e *geminiEncoder) flushTool() {
	t := e.tool
	if t == nil {
		return
	}
	e.tool = nil
	e.send(map[string]any{"functionCall": map[string]any{"name": t.name, "args": parseArgs(t.args.String()), "id": t.id}})
}

func (e *geminiEncoder) event(ev Event) {
	e.col.add(ev)
	switch ev.Kind {
	case KStart:
		if e.id == "" {
			e.id = ev.MsgID
		}
	case KText:
		e.flushTool()
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.flushTool()
		if ev.Text != "" {
			e.send(map[string]any{"text": ev.Text, "thought": true})
		}
	case KToolStart:
		e.flushTool()
		id := ev.ID
		if id == "" {
			id = "call_" + newID()
		}
		e.tool = &geminiTool{id: id, name: ev.Name}
	case KToolArgs:
		if e.tool != nil {
			e.tool.args.WriteString(ev.Text)
		}
	case KStop:
		e.flushTool()
	case KError:
		e.flushTool()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
}

func (e *geminiEncoder) finish() {
	e.flushTool()
	res := e.col.finish()
	e.w.event("", geminiBody(nil, geminiFinish(res.Stop), res.Usage, e.model, e.rid()))
}
