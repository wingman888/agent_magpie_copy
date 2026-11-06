package gateway

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Gemini's generateContent request, as far as dial reads it. Gemini is only a
// client protocol here: these requests are turned into the IR and then sent
// to whatever provider serves the model.

type gRequest struct {
	Contents          []gContent `json:"contents"`
	SystemInstruction *gContent  `json:"systemInstruction"`
	Tools             []gTool    `json:"tools"`
	ToolConfig        *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *gGenConfig `json:"generationConfig"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
}

type gPart struct {
	Text             string     `json:"text"`
	Thought          bool       `json:"thought"`
	ThoughtSignature string     `json:"thoughtSignature"`
	InlineData       *gBlob     `json:"inlineData"`
	FileData         *gFile     `json:"fileData"`
	FunctionCall     *gFuncCall `json:"functionCall"`
	FunctionResponse *gFuncResp `json:"functionResponse"`
}

type gBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type gFile struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type gFuncCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gFuncResp struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gTool struct {
	FunctionDeclarations []gFuncDecl `json:"functionDeclarations"`
}

type gFuncDecl struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
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

type gThinking struct {
	IncludeThoughts bool   `json:"includeThoughts"`
	ThinkingBudget  *int   `json:"thinkingBudget"`
	ThinkingLevel   string `json:"thinkingLevel"`
}

// parseGemini turns a Gemini generateContent body into the shared request.
// Function calls without an id get one, and a response without an id is
// paired with the latest call of the same name.
func parseGemini(body []byte) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, err
	}
	r := &Request{}
	if g.SystemInstruction != nil {
		r.System = geminiText(g.SystemInstruction.Parts)
	}
	lastCall := map[string]string{}
	for _, c := range g.Contents {
		m := Message{Role: "user"}
		if c.Role == "model" {
			m.Role = "assistant"
		}
		for _, p := range c.Parts {
			m.Parts = append(m.Parts, geminiPartIn(p, lastCall)...)
		}
		if len(m.Parts) > 0 {
			r.Messages = append(r.Messages, m)
		}
	}
	for _, t := range g.Tools {
		for _, fd := range t.FunctionDeclarations {
			schema := fd.ParametersJSONSchema
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: lowerSchema(schema)})
		}
	}
	if g.ToolConfig != nil && g.ToolConfig.FunctionCallingConfig != nil {
		fc := g.ToolConfig.FunctionCallingConfig
		switch fc.Mode {
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
		r.Temp = gc.Temperature
		r.TopP = gc.TopP
		r.Stop = gc.StopSequences
		if gc.ThinkingConfig != nil {
			geminiThinking(gc.ThinkingConfig, r)
		}
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			schema := lowerSchema(gc.ResponseJSONSchema)
			if len(schema) == 0 {
				schema = lowerSchema(gc.ResponseSchema)
			}
			r.System = jsonInstruction(r.System, schema)
		}
	}
	return r, nil
}

// geminiThinking maps thinkingConfig to a reasoning effort. A budget of -1
// lets the model decide, which is treated as medium; a budget of 0 turns
// reasoning off.
func geminiThinking(tc *gThinking, r *Request) {
	r.Thinking = tc.IncludeThoughts
	if tc.ThinkingBudget != nil {
		switch b := *tc.ThinkingBudget; {
		case b == 0:
			r.Effort = ""
		case b < 0:
			r.Effort = "medium"
		default:
			r.Effort = effortOfBudget(b)
		}
	}
	if e := effortOf(tc.ThinkingLevel); e != "" {
		r.Effort = e
	}
	if r.Effort != "" {
		r.Thinking = true
	}
}

// jsonInstruction asks for JSON in the system prompt, since the upstream has
// no JSON-mode field that every protocol shares.
func jsonInstruction(system string, schema json.RawMessage) string {
	note := "Respond only with JSON. 只输出 JSON，不要输出其他文字。"
	if len(schema) > 0 {
		note = "Respond only with JSON that conforms to the schema below. 只输出符合以下 schema 的 JSON，不要输出其他文字：\n" + string(schema)
	}
	if system == "" {
		return note
	}
	return system + "\n\n" + note
}

// geminiPartIn converts one Gemini part; lastCall remembers call ids by name.
func geminiPartIn(p gPart, lastCall map[string]string) []Part {
	switch {
	case p.FunctionCall != nil:
		fc := p.FunctionCall
		id := fc.ID
		if id == "" {
			id = "call_" + newID()
		}
		lastCall[fc.Name] = id
		return []Part{{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiArgs(fc.Args)}}
	case p.FunctionResponse != nil:
		fr := p.FunctionResponse
		id := fr.ID
		if id == "" {
			id = lastCall[fr.Name]
		}
		if id == "" {
			id = "call_" + newID()
		}
		return []Part{{Kind: ToolResult, CallID: id, Text: geminiResponseText(fr.Response)}}
	case p.InlineData != nil:
		b := p.InlineData
		if strings.HasPrefix(b.MimeType, "image/") {
			return []Part{{Kind: Image, MediaType: b.MimeType, Data: b.Data}}
		}
		return []Part{{Kind: Text, Text: "[attachment omitted: " + b.MimeType + "]"}}
	case p.FileData != nil:
		f := p.FileData
		if strings.HasPrefix(f.MimeType, "image/") {
			return []Part{{Kind: Image, MediaType: f.MimeType, URL: f.FileURI}}
		}
		return []Part{{Kind: Text, Text: "[file omitted: " + f.FileURI + "]"}}
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

// geminiArgs is a call's arguments as a JSON object; nil when there are none.
func geminiArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	return parseArgs(s)
}

// geminiResponseText is a function response's output. A response that is just
// {"output": "..."} or {"result": "..."} gives its string; anything else is
// passed on as JSON.
func geminiResponseText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) == nil && len(obj) == 1 {
		for _, k := range []string{"output", "result"} {
			var s string
			if v, ok := obj[k]; ok && json.Unmarshal(v, &s) == nil {
				return s
			}
		}
	}
	return string(raw)
}

// geminiText joins the text parts of a Gemini content.
func geminiText(parts []gPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text != "" && !p.Thought {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// lowerSchema turns Gemini's upper-case schema types (OBJECT, STRING…) into
// the lower-case JSON Schema names the upstreams expect.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return raw
	}
	lowerTypes(v)
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return b
}

func lowerTypes(v any) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if k == "type" {
				switch t := val.(type) {
				case string:
					x[k] = strings.ToLower(t)
					continue
				case []any:
					for i, e := range t {
						if s, ok := e.(string); ok {
							t[i] = strings.ToLower(s)
						}
					}
					continue
				}
			}
			lowerTypes(val)
		}
	case []any:
		for _, e := range x {
			lowerTypes(e)
		}
	}
}

// estimateTokens is a rough count, the same way the Anthropic count does it.
func estimateTokens(r *Request) int {
	n := len(r.System)
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name)
		}
	}
	for _, t := range r.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return (n + 3) / 4
}
