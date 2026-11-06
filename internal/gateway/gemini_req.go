package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ---- Gemini request → the gateway's request shape --------------------------
//
// Gemini is only ever a client protocol here: the request is read into the
// same Request the other APIs produce, and the existing upstream builders take
// it from there.

type gPart struct {
	Text             string     `json:"text"`
	Thought          bool       `json:"thought"`
	ThoughtSignature string     `json:"thoughtSignature"`
	InlineData       *gBlob     `json:"inlineData"`
	FileData         *gFile     `json:"fileData"`
	FunctionCall     *gCall     `json:"functionCall"`
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

type gCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

type gFuncResp struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type gContent struct {
	Role  string  `json:"role"`
	Parts []gPart `json:"parts"`
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
	GenerationConfig *gGenConfig `json:"generationConfig"`
}

// parseGemini reads a generateContent body. model is the catalog reference
// taken from the URL.
func parseGemini(body []byte, model string) (*Request, error) {
	var g gRequest
	if err := json.Unmarshal(body, &g); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	if len(g.Contents) == 0 {
		return nil, fmt.Errorf("contents is required")
	}
	r := &Request{Model: model, fromGemini: true}

	var sys []string
	if si := g.SystemInstruction; si != nil {
		for _, p := range si.Parts {
			if p.Text != "" && !p.Thought {
				sys = append(sys, p.Text)
			}
		}
	}

	// Calls without an id are paired with the latest call of the same name.
	lastID := map[string]string{}
	seq := 0
	newCallID := func() string {
		seq++
		return fmt.Sprintf("call_%s_%d", newID(), seq)
	}
	for _, c := range g.Contents {
		msg := Message{Role: "user"}
		if c.Role == "model" {
			msg.Role = "assistant"
		}
		for _, p := range c.Parts {
			switch {
			case p.Thought:
				if p.Text != "" || p.ThoughtSignature != "" {
					msg.Parts = append(msg.Parts, Part{Kind: Thinking, Text: p.Text, Signature: p.ThoughtSignature})
				}
			case p.FunctionCall != nil:
				fc := p.FunctionCall
				id := fc.ID
				if id == "" {
					id = newCallID()
				}
				lastID[fc.Name] = id
				msg.Parts = append(msg.Parts, Part{Kind: ToolCall, ID: id, Name: fc.Name, Args: geminiRawArgs(fc.Args)})
			case p.FunctionResponse != nil:
				fr := p.FunctionResponse
				id := fr.ID
				if id == "" {
					if id = lastID[fr.Name]; id == "" {
						id = newCallID()
						lastID[fr.Name] = id
					}
				}
				text, isErr := geminiResponseText(fr.Response)
				msg.Parts = append(msg.Parts, Part{Kind: ToolResult, CallID: id, Text: text, IsError: isErr})
			case p.InlineData != nil:
				d := p.InlineData
				if strings.HasPrefix(d.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: d.MimeType, Data: d.Data})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[attachment of type %s omitted: only images can be sent here]", d.MimeType)})
				}
			case p.FileData != nil:
				f := p.FileData
				if f.FileURI == "" {
					continue
				}
				if strings.HasPrefix(f.MimeType, "image/") {
					msg.Parts = append(msg.Parts, Part{Kind: Image, MediaType: f.MimeType, URL: f.FileURI})
				} else {
					msg.Parts = append(msg.Parts, Part{Kind: Text, Text: fmt.Sprintf("[file %s (%s)]", f.FileURI, f.MimeType)})
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
			if len(schema) == 0 {
				schema = fd.Parameters
			}
			if len(schema) > 0 {
				schema = geminiLowerSchema(schema)
			}
			r.Tools = append(r.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: schema})
		}
	}
	if tc := g.ToolConfig; tc != nil && tc.FunctionCallingConfig != nil {
		fc := tc.FunctionCallingConfig
		switch strings.ToUpper(fc.Mode) {
		case "NONE":
			r.ToolChoice = "none"
		case "ANY":
			// one allowed function: call that one; otherwise any tool will do
			if len(fc.AllowedFunctionNames) == 1 {
				r.ToolChoice = "name:" + fc.AllowedFunctionNames[0]
			} else {
				r.ToolChoice = "required"
			}
		case "AUTO":
			r.ToolChoice = "auto"
		}
	}

	if gc := g.GenerationConfig; gc != nil {
		r.MaxTokens = gc.MaxOutputTokens
		r.Temp = gc.Temperature
		r.TopP = gc.TopP
		r.Stop = gc.StopSequences
		if tc := gc.ThinkingConfig; tc != nil {
			r.Effort, r.Thinking = geminiThinking(tc)
		}
		// JSON mode: no upstream has a field for it, so ask in the system prompt.
		if strings.HasPrefix(strings.ToLower(gc.ResponseMimeType), "application/json") {
			schema := gc.ResponseJSONSchema
			if len(schema) == 0 {
				schema = gc.ResponseSchema
			}
			sys = append(sys, geminiJSONInstruction(schema))
		}
	}
	r.System = strings.Join(sys, "\n\n")
	return r, nil
}

// geminiThinking maps thinkingConfig onto a reasoning effort. A budget of -1
// lets the model decide, which is treated as medium. Budgets up to 1024 tokens
// are low, up to 8192 medium, and anything larger high. A budget of 0 turns
// thinking off. thinkingLevel names the effort directly.
func geminiThinking(tc *gThinking) (string, bool) {
	switch {
	case tc.ThinkingBudget != nil:
		n := *tc.ThinkingBudget
		switch {
		case n == 0:
			return "", false
		case n < 0:
			return "medium", true
		case n <= 1024:
			return "low", true
		case n <= 8192:
			return "medium", true
		}
		return "high", true
	case tc.ThinkingLevel != "":
		switch strings.ToUpper(tc.ThinkingLevel) {
		case "MINIMAL", "LOW":
			return "low", true
		case "HIGH":
			return "high", true
		}
		return "medium", true
	case tc.IncludeThoughts:
		return "", true
	}
	return "", false
}

// geminiJSONInstruction is the system text that stands in for responseMimeType.
func geminiJSONInstruction(schema json.RawMessage) string {
	if len(schema) == 0 || string(schema) == "null" {
		return "Respond only with a single valid JSON value, with no text outside the JSON."
	}
	return "Respond only with JSON that conforms to this JSON schema, with no text outside the JSON:\n" +
		string(geminiLowerSchema(schema))
}

// geminiRawArgs keeps a function call's args, or nil when there are none.
func geminiRawArgs(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	return json.RawMessage(s)
}

// geminiResponseText reads a function response as the text a tool result carries.
// Gemini wraps the output in an object; the conventional "output" or "result"
// string is taken out of it, anything else is passed on as JSON.
func geminiResponseText(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return s, false
	}
	_, isErr := m["error"]
	for _, k := range []string{"output", "result", "error"} {
		if v, ok := m[k]; ok {
			var str string
			if json.Unmarshal(v, &str) == nil {
				return str, isErr
			}
		}
	}
	return s, isErr
}

// geminiLowerSchema turns Gemini's upper-case schema types (OBJECT, STRING…) into
// the lower-case JSON Schema names upstreams expect, at every depth.
func geminiLowerSchema(raw json.RawMessage) json.RawMessage {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	b, err := json.Marshal(geminiLowerTypes(v))
	if err != nil {
		return raw
	}
	return b
}

func geminiLowerTypes(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if k == "type" {
				switch t := y.(type) {
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
			x[k] = geminiLowerTypes(y)
		}
		return x
	case []any:
		for i := range x {
			x[i] = geminiLowerTypes(x[i])
		}
		return x
	}
	return v
}

// geminiEstimateTokens is a rough count, about four characters to a token, for
// countTokens. Nothing is sent upstream to count.
func geminiEstimateTokens(r *Request) int {
	n := len(r.System)
	for _, m := range r.Messages {
		for _, p := range m.Parts {
			n += len(p.Text) + len(p.Args) + len(p.Name) + len(p.CallID)
		}
	}
	for _, t := range r.Tools {
		n += len(t.Name) + len(t.Description) + len(t.Schema)
	}
	return n / 4
}
