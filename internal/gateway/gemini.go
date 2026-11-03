package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// ---- Gemini generateContent (client side only) -----------------------------
//
// The Gemini API is served to clients and never called as an upstream. A
// request is parsed into the same intermediate form the other three APIs use,
// and a reply is rendered from it, so every upstream protocol works behind it.

// geminiMethods are the methods dial serves on a model.
var geminiMethods = []string{"generateContent", "streamGenerateContent", "countTokens"}

// gPart is one part of a Gemini content.
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

// gContent is one turn: a role and its parts.
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
			ThinkingBudget  *int   `json:"thinkingBudget"`
			ThinkingLevel   string `json:"thinkingLevel"`
			IncludeThoughts *bool  `json:"includeThoughts"`
		} `json:"thinkingConfig"`
	} `json:"generationConfig"`
}

// parseGemini reads a generateContent request into the shared form.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{}
	var sys []string
	if g.SystemInstruction != nil {
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != "" {
				sys = append(sys, p.Text)
			}
		}
	}

	// Gemini pairs a function response with its call by id; when the client
	// leaves ids out, the response pairs with the latest call of that name.
	calls := map[string]string{}
	n := 0
	newCallID := func() string {
		n++
		return fmt.Sprintf("call_%s_%d", newID(), n)
	}
	for _, c := range g.Contents {
		role := "user"
		if c.Role == "model" {
			role = "assistant"
		}
		var results, rest []Part
		for _, p := range c.Parts {
			switch {
			case p.Thought:
				rest = append(rest, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
			case p.Text != "":
				rest = append(rest, Part{Kind: Text, Text: p.Text})
			case p.InlineData != nil:
				if isImage(p.InlineData.MimeType) {
					rest = append(rest, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					rest = append(rest, Part{Kind: Text, Text: fmt.Sprintf("[attachment %s: inline data not forwarded]", p.InlineData.MimeType)})
				}
			case p.FileData != nil:
				fd := p.FileData
				if isImage(fd.MimeType) && fd.FileURI != "" {
					rest = append(rest, Part{Kind: Image, MediaType: fd.MimeType, URL: fd.FileURI})
				} else {
					rest = append(rest, Part{Kind: Text, Text: fmt.Sprintf("[file %s (%s)]", fd.FileURI, fd.MimeType)})
				}
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = newCallID()
				}
				calls[fc.Name] = id
				// a call's thoughtSignature has no field in the other APIs and is dropped
				rest = append(rest, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: parseArgs(string(fc.Args))})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					if prev, ok := calls[fr.Name]; ok {
						id = prev
					} else {
						id = newCallID()
					}
				}
				text, isErr := geminiResponseText(fr.Response)
				results = append(results, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			}
		}
		// results first: each one must sit right after the call it answers
		if parts := append(results, rest...); len(parts) > 0 {
			r.Messages = append(r.Messages, Message{Role: role, Parts: parts})
		}
	}

	for _, t := range g.Tools {
		for _, f := range t.FunctionDeclarations {
			schema := f.ParametersJSONSchema
			if len(schema) == 0 {
				schema = f.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: lowerSchema(schema)})
		}
	}
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "AUTO", "VALIDATED":
			r.ToolChoice = "auto"
		case "ANY":
			allowed := fc.AllowedFunctionNames
			if len(allowed) > 0 {
				kept := keepTools(r.Tools, allowed)
				if len(kept) == 0 {
					// the constraint would name no tool the upstream could call
					return nil, fmt.Errorf("allowedFunctionNames %v match no declared function", allowed)
				}
				r.Tools = kept
			}
			switch {
			case len(allowed) == 1:
				r.ToolChoice = "name:" + allowed[0]
			case len(allowed) == 0 && len(r.Tools) == 1:
				r.ToolChoice = "name:" + r.Tools[0].Name
			default:
				r.ToolChoice = "required"
			}
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP = gc.Temperature, gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			// includeThoughts governs only what the client sees: an explicit
			// true asks for visible reasoning, an explicit false hides it.
			// Unset keeps thoughts visible. The upstream thinking request
			// comes from Effort alone, so the two are judged apart.
			r.Thinking = tc.IncludeThoughts != nil && *tc.IncludeThoughts
			r.HideThoughts = tc.IncludeThoughts != nil && !*tc.IncludeThoughts
			switch {
			case tc.ThinkingBudget != nil:
				r.Effort = budgetEffort(*tc.ThinkingBudget)
			case tc.ThinkingLevel != "":
				r.Effort = effortOf(tc.ThinkingLevel)
			}
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(gc.ResponseMimeType)), "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			sys = append(sys, jsonInstruction(schema))
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// budgetEffort maps a Gemini thinking budget to an effort level. -1 lets the
// model decide, which is taken as medium.
func budgetEffort(n int) string {
	switch {
	case n < 0:
		return "medium"
	case n == 0:
		return ""
	case n <= 4096:
		return "low"
	case n <= 12000:
		return "medium"
	}
	return "high"
}

// jsonInstruction is what a JSON response mode becomes for upstreams that have
// no such switch: a system instruction that embeds the schema.
func jsonInstruction(schema json.RawMessage) string {
	const head = "Respond only with JSON. Output nothing but the JSON value itself."
	if s := lowerSchema(schema); len(s) > 0 {
		return "Respond only with JSON that conforms to the JSON schema below. Output nothing but the JSON.\nJSON schema:\n" + string(s)
	}
	return head
}

// keepTools keeps the declared tools whose names are allowed.
func keepTools(tools []Tool, allowed []string) []Tool {
	var out []Tool
	for _, t := range tools {
		if slices.Contains(allowed, t.Name) {
			out = append(out, t)
		}
	}
	return out
}

// geminiResponseText is the text of a function response. Gemini clients put
// the result under "output", or the failure under "error".
func geminiResponseText(raw json.RawMessage) (string, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) == nil {
		for _, key := range []string{"output", "error"} {
			v, ok := m[key]
			if !ok {
				continue
			}
			var s string
			if json.Unmarshal(v, &s) != nil {
				s = string(v)
			}
			return s, key == "error"
		}
	}
	if s := strings.TrimSpace(string(raw)); s != "" && s != "null" {
		return s, false
	}
	return "{}", false
}

func isImage(mime string) bool {
	return strings.HasPrefix(strings.ToLower(mime), "image/")
}

// lowerSchema turns a Gemini schema (OBJECT, STRING…) into plain JSON Schema
// (object, string…). Anything it cannot read is returned unchanged.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
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
			if s, ok := val.(string); ok && k == "type" {
				x[k] = geminiType(s)
				continue
			}
			x[k] = lowerTypes(val)
		}
	case []any:
		for i := range x {
			x[i] = lowerTypes(x[i])
		}
	}
	return v
}

func geminiType(s string) string {
	switch strings.ToUpper(s) {
	case "STRING":
		return "string"
	case "NUMBER":
		return "number"
	case "INTEGER":
		return "integer"
	case "BOOLEAN":
		return "boolean"
	case "ARRAY":
		return "array"
	case "OBJECT":
		return "object"
	case "NULL":
		return "null"
	}
	return s
}

// ---- rendering a reply -------------------------------------------------------

// geminiPart is one reply part, or nil when there is nothing to say.
func geminiPart(p Part) map[string]any {
	switch p.Kind {
	case Text:
		if p.Text != "" {
			return map[string]any{"text": p.Text}
		}
	case Thinking:
		if p.Text == "" && p.Signature == "" {
			return nil
		}
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
		return map[string]any{"functionCall": map[string]any{"id": id, "name": p.Name, "args": parseArgs(argsString(p))}}
	}
	return nil
}

// geminiFinish is the Gemini finish reason for a stop.
func geminiFinish(stop string) string {
	switch stop {
	case "length":
		return "MAX_TOKENS"
	case "filter":
		return "SAFETY"
	}
	return "STOP"
}

// geminiUsage reports tokens the way Gemini counts them. The prompt count
// includes cached tokens; candidates are the upstream's output tokens, which
// already contain any reasoning tokens.
func geminiUsage(u Usage) map[string]any {
	prompt := u.Input + u.CacheRead + u.CacheWrite
	return map[string]any{
		"promptTokenCount":        prompt,
		"candidatesTokenCount":    u.Output,
		"totalTokenCount":         prompt + u.Output,
		"cachedContentTokenCount": u.CacheRead,
		"thoughtsTokenCount":      u.Reasoning,
	}
}

// renderGemini is the non-streaming reply.
func renderGemini(res Result, model string) []byte {
	parts := []map[string]any{}
	for _, p := range res.Parts {
		if x := geminiPart(p); x != nil {
			parts = append(parts, x)
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
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": parts},
			"finishReason": geminiFinish(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
	return b
}

// geminiEncoder writes events as a Gemini stream: one event per part, in the
// order the reply made them. A function call is held until its arguments are
// complete, because Gemini sends each call whole.
type geminiEncoder struct {
	w     *sseWriter
	model string
	id    string
	open  bool // a function call is still receiving its arguments
	call  Part
	args  strings.Builder
	col   collector
}

func (e *geminiEncoder) part(p map[string]any) {
	e.w.event("", map[string]any{"candidates": []map[string]any{{
		"content": map[string]any{"role": "model", "parts": []any{p}},
	}}})
}

func (e *geminiEncoder) closeCall() {
	if !e.open {
		return
	}
	e.open = false
	e.part(map[string]any{"functionCall": map[string]any{"id": e.call.ID, "name": e.call.Name, "args": parseArgs(e.args.String())}})
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
	case KText:
		if ev.Text == "" {
			return
		}
		e.closeCall()
		e.part(map[string]any{"text": ev.Text})
	case KThink:
		if ev.Text == "" {
			return
		}
		e.closeCall()
		e.part(map[string]any{"text": ev.Text, "thought": true})
	case KSig:
		if ev.Text == "" {
			return
		}
		e.closeCall()
		e.part(map[string]any{"text": "", "thought": true, "thoughtSignature": ev.Text})
	case KToolStart:
		e.closeCall()
		id := ev.ID
		if id == "" {
			id = "call_" + newID()
		}
		e.open = true
		e.call = Part{Kind: ToolCall, ID: id, Name: ev.Name}
	case KToolArgs:
		if e.open && ev.Text != "" {
			e.args.WriteString(ev.Text)
		}
	case KError:
		e.closeCall()
		e.w.event("", map[string]any{"error": map[string]any{"code": 502, "message": ev.Text, "status": "UNAVAILABLE"}})
	}
	e.col.add(ev)
}

func (e *geminiEncoder) finish() {
	e.closeCall()
	res := e.col.finish()
	id := e.id
	if id == "" {
		id = newID()
	}
	model := e.model
	if res.Model != "" {
		model = res.Model
	}
	e.w.event("", map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": []any{}},
			"finishReason": geminiFinish(res.Stop),
			"index":        0,
		}},
		"usageMetadata": geminiUsage(res.Usage),
		"modelVersion":  model,
		"responseId":    id,
	})
}

// ---- HTTP -----------------------------------------------------------------------

// writeCompact writes a JSON body with no extra whitespace.
func writeCompact(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// geminiStatus is the gRPC-style status name Gemini gives an HTTP status.
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
	return "INTERNAL"
}

// geminiError is the error body Gemini clients expect.
func geminiError(w http.ResponseWriter, status int, msg string) int {
	writeCompact(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "status": geminiStatus(status)}})
	return status
}

// gemini serves POST /v1beta/models/{model}:{method}. The route has already
// cut off the /v1beta/models/ prefix, so rest is "fake/m1:generateContent";
// the model reference is taken as it is, even if it begins with "models/".
func (s *Server) gemini(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("rest")
	i := strings.LastIndex(ref, ":")
	if i < 0 || !slices.Contains(geminiMethods, ref[i+1:]) {
		geminiError(w, 404, fmt.Sprintf("dial serves %s on /v1beta/models/{model}", strings.Join(geminiMethods, ", ")))
		return
	}
	model, method := ref[:i], ref[i+1:]
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		geminiError(w, 400, err.Error())
		return
	}
	if method == "countTokens" {
		s.geminiCount(w, model, body)
		return
	}
	s.serve(w, r, provider.Gemini, body, model, method == "streamGenerateContent")
}

// geminiCount estimates the tokens of a request. The body is either a
// generateContent request or one wrapped in generateContentRequest.
func (s *Server) geminiCount(w http.ResponseWriter, model string, body []byte) {
	if _, _, ok := provider.Resolve(model); !ok {
		geminiError(w, 404, fmt.Sprintf("dial knows no model %q", model))
		return
	}
	var wrap struct {
		Req json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &wrap) == nil && len(bytes.TrimSpace(wrap.Req)) > 0 {
		body = wrap.Req
	}
	req, err := parseGemini(body)
	if err != nil {
		geminiError(w, 400, err.Error())
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
	writeCompact(w, 200, map[string]any{"totalTokens": (n + 3) / 4})
}

// geminiModel lists one model in the Gemini shape.
func geminiModel(e provider.Entry) map[string]any {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return map[string]any{"name": "models/" + e.ID, "version": "dial", "displayName": name,
		"supportedGenerationMethods": geminiMethods}
}

// geminiModels lists the catalog for GET /v1beta/models.
func (s *Server) geminiModels(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, e := range provider.Catalog() {
		data = append(data, geminiModel(e))
	}
	writeCompact(w, 200, map[string]any{"models": data})
}

// geminiModelInfo answers GET /v1beta/models/{model}.
func (s *Server) geminiModelInfo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("rest") // as in the route: "fake/m1", kept as it is
	for _, e := range provider.Catalog() {
		if e.ID == id {
			writeCompact(w, 200, geminiModel(e))
			return
		}
	}
	geminiError(w, 404, fmt.Sprintf("dial knows no model %q", id))
}
