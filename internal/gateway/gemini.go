package gateway

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

// This file serves the Gemini API (generateContent, streamGenerateContent,
// countTokens and the model list) so Gemini CLI can use any catalog model.
// Gemini is only the client's wire format: a request is parsed into the same
// IR the other APIs use, sent to the provider's own protocol, and the reply
// is rendered back as Gemini. No provider speaks Gemini.

// geminiFrom marks Gemini calls in the recent-calls list.
const geminiFrom = provider.Protocol("gemini")

// geminiMethods are the methods served after the ":" in a model path.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// ---- wire types -----------------------------------------------------------------

// gPart is one part of a Gemini content; only one of the fields is set.
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

type gRequest struct {
	Contents []struct {
		Role  string  `json:"role"`
		Parts []gPart `json:"parts"`
	} `json:"contents"`
	SystemInstruction json.RawMessage `json:"systemInstruction"`
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

// ---- routes -----------------------------------------------------------------------

// geminiModels lists the whole catalog in the Gemini model shape.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	list := []map[string]any{}
	for _, e := range provider.Catalog() {
		list = append(list, map[string]any{
			"name":                       "models/" + e.ID,
			"baseModelId":                e.ID,
			"version":                    "dial",
			"displayName":                e.Name,
			"description":                e.Provider.Name + " via dial",
			"supportedGenerationMethods": geminiMethods,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": list})
}

// gemini serves POST /v1beta/models/{model}:{method}. The model may contain
// slashes, so the path is split at the last colon.
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	model, method, ok := splitGeminiPath(r.PathValue("rest"))
	if !ok || !slices.Contains(geminiMethods, method) {
		writeGeminiError(w, http.StatusNotFound, "dial serves POST /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	if method == "countTokens" {
		s.geminiCount(w, model, body)
		return
	}
	stream := method == "streamGenerateContent"
	alt := r.URL.Query().Get("alt") == "sse"

	start := time.Now()
	call := Call{Time: start, From: geminiFrom, Model: model, Agent: usage.AgentOf(r.Header.Get("User-Agent"))}
	p, vendor, ok := provider.Resolve(model)
	if !ok {
		call.Status, call.Error = http.StatusNotFound, "unknown model"
		s.record(call)
		writeGeminiError(w, http.StatusNotFound, unknownModelMessage(model))
		return
	}
	call.Provider = p.ID
	req, err := parseGemini(body)
	if err != nil {
		call.Status, call.Error = http.StatusBadRequest, err.Error()
		s.record(call)
		writeGeminiError(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Model = vendor
	req.Stream = stream
	to := p.Speaks()
	if len(to) == 0 {
		writeGeminiError(w, http.StatusBadGateway, p.Name+" has no endpoint configured")
		return
	}
	call.To = to[0]
	call.Status, call.Error = s.geminiTranslate(w, r, p, to[0], vendor, model, req, stream, alt, &call.Usage)
	call.Millis = time.Since(start).Milliseconds()
	s.record(call)
	usage.Append(usage.Record{Time: start, Agent: call.Agent, Provider: p.ID, Model: vendor,
		Input: call.Usage.Input, Output: call.Usage.Output, CacheRead: call.Usage.CacheRead,
		CacheWrite: call.Usage.CacheWrite, Reasoning: call.Usage.Reasoning, Millis: call.Millis, Status: call.Status})
}

// splitGeminiPath cuts "fake/m1:generateContent" into model and method.
func splitGeminiPath(rest string) (model, method string, ok bool) {
	i := strings.LastIndex(rest, ":")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// geminiCount answers countTokens with a rough estimate: about four
// characters per token. The request may be wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeGeminiError(w, http.StatusNotFound, unknownModelMessage(model))
		return
	}
	inner := body
	var wrap struct {
		GenerateContentRequest json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && !isEmptyRaw(wrap.GenerateContentRequest) {
		inner = wrap.GenerateContentRequest
	}
	req, err := parseGemini(inner)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, err.Error())
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
	tokens := n / 4
	if n > 0 && tokens == 0 {
		tokens = 1
	}
	writeJSON(w, http.StatusOK, map[string]any{"totalTokens": tokens})
}

// geminiTranslate sends the request to the provider in its own protocol and
// writes the reply in Gemini shape: as SSE chunks, as a JSON array of chunks
// (stream without alt=sse), or as one JSON response.
func (s *Server) geminiTranslate(w http.ResponseWriter, r *http.Request, p provider.Provider, to provider.Protocol,
	model, client string, req *Request, stream, alt bool, u *Usage) (int, string) {
	req.Stream = true
	res, err := s.forward(r.Context(), p, to, pathOf(to), build(to, req, model, p.Host()), r.Header)
	if err != nil {
		return writeGeminiError(w, http.StatusBadGateway, p.Name+": "+err.Error()), err.Error()
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + ": " + provider.APIError(b, res.Status)
		return writeGeminiError(w, res.StatusCode, msg), msg
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		msg := p.Name + " did not stream: " + provider.APIError(b, "unexpected reply")
		return writeGeminiError(w, http.StatusBadGateway, msg), msg
	}
	dec := decoder(to)

	if stream {
		var chunks []any
		var emit func(any)
		var sw *sseWriter
		if alt {
			sw = newSSEWriter(w)
			emit = func(v any) { sw.event("", v) }
		} else {
			emit = func(v any) { chunks = append(chunks, v) }
		}
		enc := newGeminiEncoder(client, emit)
		var failed string
		readSSE(res.Body, func(_, data string) error {
			return dec(data, func(ev Event) {
				switch ev.Kind {
				case KError:
					failed = ev.Text
				case KStart, KUsage:
					u.add(ev.Usage)
				}
				enc.event(ev)
			})
		})
		enc.finish()
		if !alt {
			if chunks == nil {
				chunks = []any{}
			}
			writeJSON(w, http.StatusOK, chunks)
		}
		return http.StatusOK, failed
	}

	var col collector
	readSSE(res.Body, func(_, data string) error {
		return dec(data, col.add)
	})
	if col.err != "" && len(col.res.Parts) == 0 {
		return writeGeminiError(w, http.StatusBadGateway, p.Name+": "+col.err), col.err
	}
	res2 := col.finish()
	u.add(res2.Usage)
	writeJSON(w, http.StatusOK, renderGemini(res2, client))
	return http.StatusOK, col.err
}

// ---- request side ----------------------------------------------------------------

// parseGemini reads a generateContent body into the shared request.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	var sys []string
	if s := geminiSystem(g.SystemInstruction); s != "" {
		sys = append(sys, s)
	}
	// calls remembers the ids of the calls made so far, by function name, so a
	// result without an id can be matched to the latest call of that name.
	calls := map[string][]string{}
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			msg.Parts = append(msg.Parts, geminiPart(p, calls)...)
		}
		if len(msg.Parts) > 0 {
			r.Messages = append(r.Messages, msg)
		}
	}
	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.ParametersJSONSchema
			if isEmptyRaw(schema) {
				schema = fd.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: geminiSchema(schema)})
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
			// a single allowed name can be forced; a longer list cannot be expressed
			r.ToolChoice = "required"
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			}
		}
	}
	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP, r.Stop = gc.Temperature, gc.TopP, gc.StopSequences
		if th := gc.ThinkingConfig; th != nil {
			r.Thinking = th.IncludeThoughts
			if th.ThinkingBudget != nil {
				switch b := *th.ThinkingBudget; {
				case b < 0: // the model decides; treat it as medium effort
					r.Effort = "medium"
				case b > 0:
					r.Effort = effortOfBudget(b)
				}
			} else {
				r.Effort = geminiLevel(th.ThinkingLevel)
			}
		}
		schema := gc.ResponseJSONSchema
		if isEmptyRaw(schema) {
			schema = gc.ResponseSchema
		}
		if gc.ResponseMimeType == "application/json" || !isEmptyRaw(schema) {
			// no provider has a JSON mode in the shared IR; ask for it in words
			note := "Reply with only one valid JSON value: no prose, no markdown, no code fences."
			if s := geminiSchema(schema); s != nil {
				note += " The JSON must conform to this JSON Schema: " + string(s)
			}
			sys = append(sys, note)
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// geminiSystem reads systemInstruction, an object of parts or a bare string.
func geminiSystem(raw json.RawMessage) string {
	if isEmptyRaw(raw) {
		return ""
	}
	var c struct {
		Parts []gPart `json:"parts"`
	}
	if json.Unmarshal(raw, &c) == nil {
		var texts []string
		for _, p := range c.Parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "\n")
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// geminiPart converts one Gemini part into IR parts.
func geminiPart(p gPart, calls map[string][]string) []Part {
	switch {
	case p.FunctionCall != nil:
		fc := p.FunctionCall
		id := fc.ID
		if id == "" {
			id = "call_" + newID()
		}
		calls[fc.Name] = append(calls[fc.Name], id)
		return []Part{{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiArgs(fc.Args)}}
	case p.FunctionResponse != nil:
		fr := p.FunctionResponse
		id := fr.ID
		if id == "" {
			if ids := calls[fr.Name]; len(ids) > 0 {
				id = ids[len(ids)-1]
			} else {
				id = "call_" + newID()
			}
		}
		text, failed := geminiResponse(fr.Response)
		return []Part{{Kind: ToolResult, CallID: id, Text: text, IsError: failed}}
	case p.InlineData != nil:
		d := p.InlineData
		if strings.HasPrefix(d.MimeType, "image/") {
			return []Part{{Kind: Image, MediaType: d.MimeType, Data: d.Data}}
		}
		return []Part{{Kind: Text, Text: fmt.Sprintf("[%s attachment omitted: dial forwards only images]", d.MimeType)}}
	case p.FileData != nil:
		f := p.FileData
		if strings.HasPrefix(f.MimeType, "image/") && f.FileURI != "" {
			return []Part{{Kind: Image, MediaType: f.MimeType, URL: f.FileURI}}
		}
		return []Part{{Kind: Text, Text: fmt.Sprintf("[file %s omitted: dial forwards only images]", f.FileURI)}}
	case p.Thought:
		if p.Text == "" && p.ThoughtSignature == "" {
			return nil
		}
		return []Part{{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature}}
	case p.Text != "":
		return []Part{{Kind: Text, Text: p.Text}}
	}
	return nil
}

// geminiArgs is a function call's args as a JSON object.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	if isEmptyRaw(raw) {
		return json.RawMessage("{}")
	}
	return parseArgs(string(raw))
}

// geminiResponse reads a functionResponse's response: the "output" string when
// that is all it holds, otherwise the JSON as it is. It reports an error when
// the response carries an "error" key.
func geminiResponse(raw json.RawMessage) (string, bool) {
	if isEmptyRaw(raw) {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		if out, ok := m["output"]; ok && len(m) == 1 {
			var o string
			if json.Unmarshal(out, &o) == nil {
				return o, false
			}
		}
		_, failed := m["error"]
		return strings.TrimSpace(string(raw)), failed
	}
	return strings.TrimSpace(string(raw)), false
}

// geminiLevel maps thinkingLevel to an effort.
func geminiLevel(s string) string {
	switch strings.ToUpper(s) {
	case "MINIMAL", "LOW":
		return "low"
	case "MEDIUM":
		return "medium"
	case "HIGH":
		return "high"
	}
	return ""
}

// geminiSchema turns a Gemini schema (upper-case type names) into a standard
// lower-case JSON Schema, recursively. It returns nil for an empty schema.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if isEmptyRaw(raw) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	b, err := json.Marshal(lowerSchema(v))
	if err != nil {
		return nil
	}
	return b
}

// lowerSchema lower-cases every "type" in a decoded schema, and turns
// "nullable": true into a null-accepting type list.
func lowerSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "type" {
				x[k] = strings.ToLower(s)
				continue
			}
			x[k] = lowerSchema(val)
		}
		if nullable, _ := x["nullable"].(bool); nullable {
			delete(x, "nullable")
			if t, ok := x["type"].(string); ok {
				x["type"] = []any{t, "null"}
			}
		}
		return x
	case []any:
		for i := range x {
			x[i] = lowerSchema(x[i])
		}
		return x
	}
	return v
}

func isEmptyRaw(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null"
}

func unknownModelMessage(model string) string {
	msg := fmt.Sprintf("dial knows no model %q", model)
	if ids := provider.IDs(); len(ids) > 0 {
		msg += "; it has " + strings.Join(ids, ", ")
	} else {
		msg += "; add a provider in dial first"
	}
	return msg
}

// ---- reply side ------------------------------------------------------------------

// geminiCall is a function call being streamed; it is emitted once complete.
type geminiCall struct {
	id, name string
	args     strings.Builder
}

// geminiEncoder writes events as Gemini stream chunks. A function call goes out
// whole, when the upstream says its arguments are finished. The last chunk
// carries finishReason and usageMetadata.
type geminiEncoder struct {
	emit   func(any)
	model  string
	id     string
	usage  Usage
	stop   string
	failed string
	open   *geminiCall
}

func newGeminiEncoder(model string, emit func(any)) *geminiEncoder {
	return &geminiEncoder{emit: emit, model: model, id: "dial-" + newID()}
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
		e.usage.add(ev.Usage)
	case KText:
		e.closeCall()
		if ev.Text != "" {
			e.part(map[string]any{"text": ev.Text})
		}
	case KThink:
		e.closeCall()
		if ev.Text != "" {
			e.part(map[string]any{"text": ev.Text, "thought": true})
		}
	case KSig:
		e.closeCall()
		if ev.Text != "" {
			e.part(map[string]any{"text": "", "thought": true, "thoughtSignature": ev.Text})
		}
	case KToolStart:
		e.closeCall()
		id := ev.ID
		if id == "" {
			id = "call_" + newID()
		}
		e.open = &geminiCall{id: id, name: ev.Name}
	case KToolArgs:
		if e.open != nil {
			e.open.args.WriteString(ev.Text)
		}
	case KStop:
		e.closeCall()
		e.stop = ev.Stop
	case KUsage:
		e.usage.add(ev.Usage)
	case KError:
		e.failed = ev.Text
	}
}

// closeCall emits the open function call, now complete.
func (e *geminiEncoder) closeCall() {
	c := e.open
	if c == nil {
		return
	}
	e.open = nil
	e.part(map[string]any{"functionCall": map[string]any{"id": c.id, "name": c.name, "args": parseArgs(c.args.String())}})
}

func (e *geminiEncoder) part(p map[string]any) {
	e.emit(e.chunk([]any{p}, ""))
}

func (e *geminiEncoder) chunk(parts []any, finish string) map[string]any {
	if parts == nil {
		parts = []any{}
	}
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts}, "index": 0}
	if finish != "" {
		cand["finishReason"] = finish
	}
	return map[string]any{"candidates": []any{cand}, "modelVersion": e.model, "responseId": e.id}
}

// finish writes the closing chunk, or an error object when the upstream failed.
func (e *geminiEncoder) finish() {
	e.closeCall()
	if e.failed != "" {
		e.emit(geminiErrorBody(http.StatusBadGateway, e.failed))
		return
	}
	c := e.chunk(nil, geminiFinishReason(e.stop))
	c["usageMetadata"] = geminiUsage(e.usage)
	e.emit(c)
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) map[string]any {
	parts := []any{}
	for _, p := range res.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				parts = append(parts, map[string]any{"text": p.Text})
			}
		case Thinking:
			if p.Text == "" && p.Signature == "" {
				continue
			}
			m := map[string]any{"text": p.Text, "thought": true}
			if p.Signature != "" {
				m["thoughtSignature"] = p.Signature
			}
			parts = append(parts, m)
		case ToolCall:
			id := p.ID
			if id == "" {
				id = "call_" + newID()
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": argsOf(p)}})
		}
	}
	id := res.ID
	if id == "" {
		id = "dial-" + newID()
	}
	return map[string]any{
		"candidates": []any{map[string]any{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinishReason(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	}
}

// geminiUsage maps the shared usage onto Gemini's counts. The prompt count
// includes cache hits; reasoning tokens are counted apart from the candidates.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead
	cand := u.Output - u.Reasoning
	if cand < 0 {
		cand = 0
	}
	m := map[string]any{
		"promptTokenCount":     prompt,
		"candidatesTokenCount": cand,
		"totalTokenCount":      prompt + cand + u.Reasoning,
	}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	if u.Reasoning > 0 {
		m["thoughtsTokenCount"] = u.Reasoning
	}
	return m
}

func geminiFinishReason(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiStatus is the gRPC-style status name for an HTTP code.
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
	if code >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}

func geminiErrorBody(code int, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": msg, "status": geminiStatus(code)}}
}

// writeGeminiError answers in the Gemini error shape.
func writeGeminiError(w http.ResponseWriter, status int, msg string) int {
	writeJSON(w, status, geminiErrorBody(status, msg))
	return status
}
