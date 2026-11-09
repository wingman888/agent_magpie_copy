package gateway

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ---- Gemini (client protocol only) --------------------------------------------
//
// Gemini is served as a client protocol, never as an upstream. A Gemini
// request is parsed into the shared Request here; the existing build* functions
// then render it for whichever upstream serves the model. The reply comes back
// through renderGemini or geminiEncoder (gemini_render.go).

type gPart struct {
	Text             string     `json:"text"`
	Thought          bool       `json:"thought"`
	InlineData       *gBlob     `json:"inlineData"`
	FileData         *gFile     `json:"fileData"`
	FunctionCall     *gCall     `json:"functionCall"`
	FunctionResponse *gResponse `json:"functionResponse"`
}

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

type gResponse struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
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

type gFuncConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames"`
}

type gThinking struct {
	ThinkingBudget  *int   `json:"thinkingBudget"`
	ThinkingLevel   string `json:"thinkingLevel"`
	IncludeThoughts bool   `json:"includeThoughts"`
}

type gGenConfig struct {
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
	Contents          []gContent  `json:"contents"`
	SystemInstruction *gContent   `json:"systemInstruction"`
	Tools             []gTool     `json:"tools"`
	ToolConfig        *struct {
		FunctionCallingConfig *gFuncConfig `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *gGenConfig `json:"generationConfig"`
}

// parseGemini reads a generateContent request body. The model comes from the
// URL path and stream from the method name, neither from the body.
func parseGemini(model string, stream bool, body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	r := &Request{Model: model, Stream: stream}

	// Calls without an id are paired with responses by function name, using
	// the most recent call of that name.
	calls := map[string]string{}
	seq := 0
	newCallID := func() string {
		seq++
		return fmt.Sprintf("call_%s_%d", newID(), seq)
	}

	var sys []string
	if g.SystemInstruction != nil {
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != "" {
				sys = append(sys, p.Text)
			}
		}
	}

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
				calls[p.FunctionCall.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: geminiArgs(p.FunctionCall.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					id = calls[fr.Name]
				}
				if id == "" {
					id = newCallID()
				}
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: geminiResponseText(fr.Response)})
			case p.Thought:
				if p.Text != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text})
				}
			case p.Text != "":
				msg.Parts = append(msg.Parts, Part{Kind: Text, Text: p.Text})
			case p.InlineData != nil:
				if isImageType(p.InlineData.MimeType) {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment of type %s omitted]", p.InlineData.MimeType)})
				}
			case p.FileData != nil:
				if isImageType(p.FileData.MimeType) {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: p.FileData.MimeType, URL: p.FileData.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file %s (%s)]", p.FileData.FileURI, p.FileData.MimeType)})
				}
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
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: geminiSchema(schema)})
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
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp, r.TopP, r.Stop = gc.Temperature, gc.TopP, gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			r.Effort = geminiEffort(tc)
			r.Thinking = tc.IncludeThoughts || r.Effort != ""
		}
		// Upstreams have no JSON-mode field everywhere, so the schema goes into
		// the system prompt instead.
		if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			add := "只输出合法的 JSON，不要输出其他内容。"
			if s := geminiSchema(schema); len(s) > 0 {
				add = "只输出符合以下 schema 的 JSON，不要输出其他内容：\n" + string(s)
			}
			sys = append(sys, add)
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// geminiCountBody unwraps a countTokens body that wraps the request in
// generateContentRequest; otherwise the body is the request itself.
func geminiCountBody(body []byte) []byte {
	var w struct {
		G json.RawMessage `json:"generateContentRequest"`
	}
	if json.Unmarshal(body, &w) == nil && len(w.G) > 0 && string(w.G) != "null" {
		return w.G
	}
	return body
}

// geminiEffort maps thinkingConfig onto the reasoning effort levels. A budget
// of -1 lets the model decide, which is treated as medium; 0 asks for the
// lowest effort the upstreams take.
func geminiEffort(tc *gThinking) string {
	if tc.ThinkingBudget != nil {
		switch b := *tc.ThinkingBudget; {
		case b < 0:
			return "medium"
		case b <= 4096:
			return "low"
		case b <= 12000:
			return "medium"
		default:
			return "high"
		}
	}
	switch strings.ToUpper(tc.ThinkingLevel) {
	case "MINIMAL", "LOW":
		return "low"
	case "MEDIUM":
		return "medium"
	case "HIGH":
		return "high"
	}
	return ""
}

// geminiArgs is a function call's args as a JSON object, never empty.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return parseArgs(s)
}

// geminiResponseText is a function response's text. A response of the form
// {"output": "..."} gives its string; anything else is sent as compact JSON.
func geminiResponseText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		if s, ok := m["output"].(string); ok {
			return s
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func isImageType(mt string) bool {
	return strings.HasPrefix(strings.ToLower(mt), "image/")
}

// geminiTypes are the upper-case schema type names Gemini uses, with the
// lower-case names JSON Schema and the upstreams expect.
var geminiTypes = map[string]string{
	"OBJECT": "object", "STRING": "string", "INTEGER": "integer",
	"NUMBER": "number", "BOOLEAN": "boolean", "ARRAY": "array", "NULL": "null",
}

// geminiSchema lowers the type names of a schema, recursively. It returns nil
// for an empty schema and the input unchanged if it is not JSON.
func geminiSchema(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	b, _ := json.Marshal(lowerSchema(v))
	return b
}

func lowerSchema(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "type" {
				x[k] = lowerType(val)
			} else {
				x[k] = lowerSchema(val)
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

// lowerType handles the value of a "type" key. A property that happens to be
// named "type" has a schema object as its value, which lowerSchema handles.
func lowerType(v any) any {
	switch x := v.(type) {
	case string:
		if l, ok := geminiTypes[x]; ok {
			return l
		}
		return x
	case []any:
		for i := range x {
			x[i] = lowerType(x[i])
		}
		return x
	}
	return lowerSchema(v)
}
