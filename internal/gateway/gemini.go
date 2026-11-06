package gateway

// Google's Gemini generateContent API, served as a client protocol: a Gemini
// client names a catalog model in the URL, the request is read into the same
// shape the other client APIs are read into, and the reply is written back in
// Gemini's shape. Gemini is never an upstream; no provider speaks it.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// geminiProto is the client protocol name for the Gemini API. It is not one of
// provider.Protocols: providers never speak it, so it is only ever a `from`.
const geminiProto provider.Protocol = "gemini"

// geminiStreamKey and the envelope's "model" carry what the URL says (the
// method and the model) into parseGemini, since a Gemini body says neither.
const geminiStreamKey = "dialStream"

var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// ---- routes -------------------------------------------------------------------

// geminiModels lists the catalog in Gemini's shape.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	models := []map[string]any{}
	for _, e := range provider.Catalog() {
		models = append(models, map[string]any{"name": "models/" + e.ID, "version": Version,
			"displayName": e.Name, "supportedGenerationMethods": geminiMethods})
	}
	writeCompact(w, http.StatusOK, map[string]any{"models": models})
}

// geminiCall handles POST /v1beta/models/{model}:{method}. The model reference
// may itself contain slashes and colons; the method is what follows the last
// colon.
func (s *Server) geminiCall(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	ref := r.PathValue("ref")
	i := strings.LastIndex(ref, ":")
	if i <= 0 {
		writeError(w, geminiProto, 404, "dial serves Gemini calls at /v1beta/models/{model}:generateContent, :streamGenerateContent and :countTokens")
		return
	}
	model, method := ref[:i], ref[i+1:]
	switch method {
	case "generateContent":
		s.serve(w, r, geminiProto, model, geminiEnvelope(body, model, false))
	case "streamGenerateContent":
		// always answered as server-sent events, which is what alt=sse asks for
		s.serve(w, r, geminiProto, model, geminiEnvelope(body, model, true))
	case "countTokens":
		s.geminiCount(w, model, body)
	default:
		writeError(w, geminiProto, 404, fmt.Sprintf("dial has no Gemini method %q for model %s", method, model))
	}
}

// geminiEnvelope adds the model and the stream flag to a Gemini body. A body
// that is not an object is returned as it was, and fails to parse later.
func geminiEnvelope(body []byte, model string, stream bool) []byte {
	m, err := decodeObject(body)
	if err != nil {
		return body
	}
	m["model"] = model
	m[geminiStreamKey] = stream
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// geminiCount estimates the tokens of a request. The body may be the request
// itself or the request wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		writeError(w, geminiProto, 404, fmt.Sprintf("dial knows no model %q", model))
		return
	}
	var wrap struct {
		Request json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(wrap.Request) > 0 {
		body = wrap.Request
	}
	req, err := parseGemini(body)
	if err != nil {
		writeError(w, geminiProto, 400, err.Error())
		return
	}
	chars, images := len(req.System), 0
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			chars += len(p.Text) + len(p.Args) + len(p.Name) + len(p.CallID)
			if p.Kind == Image {
				images++
			}
		}
	}
	for _, t := range req.Tools {
		chars += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	// about four characters to a token, and a fixed allowance per image
	writeCompact(w, http.StatusOK, map[string]int{"totalTokens": chars/4 + 1000*images})
}

// ---- request ------------------------------------------------------------------

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

type gRequest struct {
	Model             string     `json:"model"`
	Stream            bool       `json:"dialStream"`
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

// parseGemini reads a Gemini generateContent request into the shared IR.
// Function calls and responses are paired by id; a call without one gets a
// fresh id, and a response without one takes the id of the latest call of the
// same function name.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: g.Model, Stream: g.Stream}
	var sys []string
	if g.SystemInstruction != nil {
		var texts []string
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != nil && !p.Thought && *p.Text != "" {
				texts = append(texts, *p.Text)
			}
		}
		if len(texts) > 0 {
			sys = append(sys, strings.Join(texts, "\n\n"))
		}
	}

	lastCall := map[string]string{} // function name -> id of its latest call
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
				lastCall[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: argsOfRaw(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = lastCall[fr.Name]
				}
				if id == "" {
					id = "call_" + newID()
				}
				out, isErr := responseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: out, IsError: isErr})
			case p.InlineData != nil:
				d := p.InlineData
				if strings.HasPrefix(d.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: d.MimeType, Data: d.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attached file of type " + d.MimeType + " omitted]"})
				}
			case p.FileData != nil:
				f := p.FileData
				if strings.HasPrefix(f.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: f.MimeType, URL: f.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: "[attached file " + f.FileURI + " omitted]"})
				}
			case p.Thought:
				if p.Text != nil && *p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: *p.Text, Signature: p.ThoughtSignature})
				}
			case p.Text != nil && *p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: *p.Text})
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
				schema = fd.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: lowerTypes(schema)})
		}
	}
	if cfg := g.ToolConfig; cfg != nil && cfg.FunctionCallingConfig != nil {
		fc := cfg.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO":
			r.ToolChoice = "auto"
		case "ANY":
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			sys = append(sys, jsonInstruction(lowerTypes(schema)))
		}
		if tc := gc.ThinkingConfig; tc != nil {
			r.Effort = geminiEffort(tc.ThinkingBudget, tc.ThinkingLevel)
			r.Thinking = tc.IncludeThoughts || r.Effort != ""
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// argsOfRaw is a function call's args as a JSON object, "{}" when absent.
func argsOfRaw(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

// responseText is what a function response tells the model: its "output" (or
// "error") when it has one, otherwise the whole response as JSON.
func responseText(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return string(raw), false
	}
	if e, ok := m["error"]; ok {
		return jsonText(e), true
	}
	if o, ok := m["output"]; ok {
		return jsonText(o), false
	}
	return string(raw), false
}

// jsonText is a JSON string's value, or the JSON itself for anything else.
func jsonText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// geminiEffort maps thinkingConfig to a reasoning effort. A budget of -1 lets
// the model decide, which is taken as medium; 0 turns reasoning off.
func geminiEffort(budget *int, level string) string {
	if budget != nil {
		switch n := *budget; {
		case n == 0:
			return ""
		case n < 0:
			return "medium"
		default:
			return effortOfBudget(n)
		}
	}
	switch strings.ToUpper(level) {
	case "MINIMAL", "LOW":
		return "low"
	case "MEDIUM":
		return "medium"
	case "HIGH":
		return "high"
	}
	return ""
}

// jsonInstruction asks for JSON output, for upstreams with no JSON mode.
func jsonInstruction(schema json.RawMessage) string {
	if len(schema) == 0 {
		return "Output only a single valid JSON value."
	}
	return "Output only JSON that conforms to this JSON schema:\n" + string(schema)
}

// lowerTypes turns Gemini's upper-case schema types (OBJECT, STRING…) into the
// lower-case names JSON schema uses. Any other content is left alone.
func lowerTypes(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	if v == nil {
		return nil
	}
	out, err := json.Marshal(lowerWalk(v))
	if err != nil {
		return raw
	}
	return out
}

func lowerWalk(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if s, ok := e.(string); ok && k == "type" {
				x[k] = strings.ToLower(s)
				continue
			}
			x[k] = lowerWalk(e)
		}
		return x
	case []any:
		for i := range x {
			x[i] = lowerWalk(x[i])
		}
		return x
	}
	return v
}

// ---- reply --------------------------------------------------------------------

func stopToGemini(s string) string {
	switch s {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// gemini is the usage block of a reply. The prompt count includes cached
// tokens, as the other client APIs' usage does.
func (u Usage) gemini() map[string]any {
	in := u.Input + u.CacheRead
	m := map[string]any{"promptTokenCount": in, "candidatesTokenCount": u.Output,
		"totalTokenCount": in + u.Output, "thoughtsTokenCount": u.Reasoning}
	if u.CacheRead > 0 {
		m["cachedContentTokenCount"] = u.CacheRead
	}
	return m
}

// geminiReply wraps one candidate in a response, with its usage when given.
func geminiReply(cand map[string]any, usage map[string]any, model, id string) map[string]any {
	out := map[string]any{"candidates": []map[string]any{cand}}
	if usage != nil {
		out["usageMetadata"] = usage
	}
	if model != "" {
		out["modelVersion"] = model
	}
	if id != "" {
		out["responseId"] = id
	}
	return out
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
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": parts},
		"finishReason": stopToGemini(res.Stop), "index": 0}
	b, _ := json.Marshal(geminiReply(cand, res.Usage.gemini(), model, id))
	return b
}

// geminiEncoder writes events as a Gemini stream: one event per part, in the
// order they arrive, and a last event with the finish reason and usage. A
// function call is only complete once its arguments are, so it goes out when
// the next part starts or the reply ends.
type geminiEncoder struct {
	w        *sseWriter
	model    string
	id       string
	col      collector
	toolOpen bool
	toolID   string
	toolName string
	toolArgs strings.Builder
}

func (e *geminiEncoder) chunk(part map[string]any) {
	cand := map[string]any{"content": map[string]any{"role": "model", "parts": []map[string]any{part}}, "index": 0}
	e.w.event("", geminiReply(cand, nil, e.model, e.replyID()))
}

func (e *geminiEncoder) replyID() string {
	if e.id == "" {
		e.id = "dial-" + newID()
	}
	return e.id
}

func (e *geminiEncoder) flush() {
	if !e.toolOpen {
		return
	}
	e.toolOpen = false
	id := e.toolID
	if id == "" {
		id = "call_" + newID()
	}
	e.chunk(map[string]any{"functionCall": map[string]any{"id": id, "name": e.toolName, "args": parseArgs(e.toolArgs.String())}})
}

func (e *geminiEncoder) event(ev Event) {
	switch ev.Kind {
	case KStart:
		if ev.MsgID != "" {
			e.id = ev.MsgID
		}
	case KThink:
		e.flush()
		if ev.Text != "" {
			e.chunk(map[string]any{"text": ev.Text, "thought": true})
		}
	case KText:
		e.flush()
		if ev.Text != "" {
			e.chunk(map[string]any{"text": ev.Text})
		}
	case KToolStart:
		e.flush()
		e.toolOpen, e.toolID, e.toolName = true, ev.ID, ev.Name
		e.toolArgs.Reset()
	case KToolArgs:
		if e.toolOpen {
			e.toolArgs.WriteString(ev.Text)
		}
	case KError:
		e.flush()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": geminiStatus(502)}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.flush()
	res := e.col.finish()
	cand := map[string]any{"content": map[string]any{"role": "model"}, "finishReason": stopToGemini(res.Stop), "index": 0}
	e.w.event("", geminiReply(cand, res.Usage.gemini(), e.model, e.replyID()))
}

// ---- errors -------------------------------------------------------------------

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
	case 504:
		return "DEADLINE_EXCEEDED"
	}
	if code >= 500 {
		return "INTERNAL"
	}
	return "UNKNOWN"
}

// geminiErrorBody is the error shape Gemini clients read.
func geminiErrorBody(status int, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}}
}

// writeCompact writes a JSON value without the trailing newline json.Encoder adds.
func writeCompact(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}
