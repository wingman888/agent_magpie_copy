package provider

import (
	"bytes"
	"encoding/json"
	"strings"
)

// prepareCodex rewrites a Responses request into the form the ChatGPT backend
// accepts: it must stream, must not store, takes input as a list of items, and
// refuses the sampling and output-limit fields. Reasoning settings are kept.
// The model loses its "codex/" prefix. A body that is not a JSON object is
// returned unchanged.
func prepareCodex(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	if model, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(model, "codex/")
	}
	m["stream"] = true
	m["store"] = false
	switch in := m["input"].(type) {
	case nil:
		m["input"] = []any{}
	case string:
		m["input"] = []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": in}},
		}}
	}
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "max_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
