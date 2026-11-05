package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// This file turns a Gemini generateContent request into the gateway's
// common request form, so the existing builders can render it for any
// upstream. Gemini is a client protocol only: nothing here talks to a
// provider in Gemini's own shape.

// gemReq is the part of a Gemini request dial reads.
type gemReq struct {
	Contents          []gemContent `json:"contents"`
	SystemInstruction *gemContent  `json:"systemInstruction"`
	Tools             []gemTool    `json:"tools"`
	ToolConfig        *struct {
		FunctionCallingConfig *struct {
			Mode                 string   `json:"mode"`
			AllowedFunctionNames []string `json:"allowedFunctionNames"`
		} `json:"functionCallingConfig"`
	} `json:"toolConfig"`
	GenerationConfig *gemGenConfig `json:"generationConfig"`
}

type gemContent struct {
	Role  string    `json:"role"`
	Parts []gemPart `json:"parts"`
}

type gemPart struct {
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

type gemTool struct {
	FunctionDeclarations []gemFunc `json:"functionDeclarations"`
}

type gemFunc struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema"`
}

type gemThinking struct {
	ThinkingBudget  *int   `json:"thinkingBudget"`
	ThinkingLevel   string `json:"thinkingLevel"`
	IncludeThoughts bool   `json:"includeThoughts"`
}

type gemGenConfig struct {
	MaxOutputTokens    int             `json:"maxOutputTokens"`
	Temperature        *float64        `json:"temperature"`
	TopP               *float64        `json:"topP"`
	StopSequences      []string        `json:"stopSequences"`
	ResponseMimeType   string          `json:"responseMimeType"`
	ResponseSchema     json.RawMessage `json:"responseSchema"`
	ResponseJSONSchema json.RawMessage `json:"responseJsonSchema"`
	ThinkingConfig     *gemThinking    `json:"thinkingConfig"`
}

// parseGemini reads a generateContent body. A countTokens body may wrap the
// request in generateContentRequest; that wrapper is unwrapped here.
func parseGemini(body []byte) (*Request, error) {
	var probe struct {
		Wrapped json.RawMessage `json:"generateContentRequest"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return nil, err
	}
	src := body
	if len(probe.Wrapped) > 0 && string(probe.Wrapped) != "null" {
		src = probe.Wrapped
	}
	var g gemReq
	if err := json.Unmarshal(src, &g); err != nil {
		return nil, err
	}

	r := &Request{}
	if g.SystemInstruction != nil {
		var sys []string
		for _, p := range g.SystemInstruction.Parts {
			if p.Text != "" && !p.Thought {
				sys = append(sys, p.Text)
			}
		}
		r.System = strings.Join(sys, "\n")
	}

	// Function calls and responses pair up by id. Without ids, a response
	// pairs with the latest call of the same name.
	latest := map[string]string{}
	n := 0
	genID := func(name string) string {
		n++
		return fmt.Sprintf("call_%d_%s", n, name)
	}
	for _, c := range g.Contents {
		m := Message{Role: "user"}
		if c.Role == "model" {
			m.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = genID(fc.Name)
				}
				latest[fc.Name] = id
				m.Parts = append(m.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: objectOrEmpty(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					if id = latest[fr.Name]; id == "" {
						id = genID(fr.Name)
					}
				}
				text, isErr := responseText(fr.Response)
				m.Parts = append(m.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			case p.Thought:
				if p.Text != "" || p.ThoughtSignature != "" {
					m.Parts = append(m.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
				}
			case p.InlineData != nil:
				mt := p.InlineData.MimeType
				if strings.HasPrefix(mt, "image/") {
					m.Parts = append(m.Parts, Part{Kind: Image, MediaType: mt, Data: p.InlineData.Data})
				} else {
					m.Parts = append(m.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment: %s]", mt)})
				}
			case p.FileData != nil:
				mt, uri := p.FileData.MimeType, p.FileData.FileURI
				if strings.HasPrefix(mt, "image/") {
					m.Parts = append(m.Parts, Part{Kind: Image, MediaType: mt, URL: uri})
				} else {
					m.Parts = append(m.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file: %s (%s)]", uri, mt)})
				}
			case p.Text != "":
				m.Parts = append(m.Parts, Part{Kind: Text, Text: p.Text})
			}
		}
		if len(m.Parts) > 0 {
			r.Messages = append(r.Messages, m)
		}
	}

	for _, t := range g.Tools {
		for _, f := range t.FunctionDeclarations {
			schema := f.Parameters
			if len(schema) == 0 {
				schema = f.ParametersJSONSchema
			}
			r.Tools = append(r.Tools, Tool{Name: f.Name, Description: f.Description, Schema: lowerSchema(schema)})
		}
	}

	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
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
		r.Temp = gc.Temperature
		r.TopP = gc.TopP
		r.Stop = gc.StopSequences
		if strings.HasPrefix(gc.ResponseMimeType, "application/json") {
			schema := gc.ResponseSchema
			if len(schema) == 0 {
				schema = gc.ResponseJSONSchema
			}
			r.System = joinNonEmpty(r.System, jsonModeInstruction(schema))
		}
		r.Effort, r.Thinking = thinkingOf(gc.ThinkingConfig)
	}
	return r, nil
}

// thinkingOf maps Gemini's thinking config to a reasoning effort. A budget
// of 0 turns thinking off; -1 lets the model decide, which dial reads as
// medium.
func thinkingOf(tc *gemThinking) (string, bool) {
	if tc == nil {
		return "", false
	}
	if tc.ThinkingBudget != nil {
		switch b := *tc.ThinkingBudget; {
		case b == 0:
			return "", false
		case b < 0:
			return "medium", true
		case b <= 4096:
			return "low", true
		case b <= 12000:
			return "medium", true
		default:
			return "high", true
		}
	}
	switch strings.ToUpper(tc.ThinkingLevel) {
	case "MINIMAL", "LOW":
		return "low", true
	case "MEDIUM":
		return "medium", true
	case "HIGH":
		return "high", true
	}
	return "", tc.IncludeThoughts
}

// jsonModeInstruction asks for JSON in the system prompt, since the
// upstream may have no field for it. The schema, when given, is embedded.
func jsonModeInstruction(schema json.RawMessage) string {
	const head = "Respond only with a single valid JSON value, with no prose or code fences around it."
	if len(schema) == 0 {
		return head
	}
	return head + " It must conform to this JSON schema:\n" + string(lowerSchema(schema))
}

func joinNonEmpty(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n\n" + b
}

// lowerSchema turns Gemini's upper-case schema types (OBJECT, STRING…) into
// the lower-case names JSON schema uses. An empty schema becomes an empty
// object schema.
func lowerSchema(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || v == nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	out, err := json.Marshal(lowerTypes(v))
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return out
}

func lowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && k == "type" {
				x[k] = strings.ToLower(s)
				continue
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

// responseText is what a function response carries back to the model. A
// response that is just {"output": "..."} is sent as that string.
func responseText(raw json.RawMessage) (string, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "{}", false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return string(raw), false
	}
	_, isErr := obj["error"]
	if out, ok := obj["output"]; ok && len(obj) == 1 {
		var s string
		if json.Unmarshal(out, &s) == nil {
			return s, isErr
		}
	}
	return string(raw), isErr
}

func objectOrEmpty(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// estimateTokens is a rough count for countTokens: about four characters
// to a token over the text, tool arguments and tool definitions.
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
	return n / 4
}
