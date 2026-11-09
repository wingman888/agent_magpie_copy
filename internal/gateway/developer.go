package gateway

import (
	"bytes"
	"encoding/json"
)

// developerToSystem rewrites the "developer" role to "system" in a Chat
// Completions body. Reasoning models are sent developer messages by some
// agents, but Chat Completions backends such as DeepSeek reject that role.
// Nothing else changes; a body without developer messages is returned as is.
func developerToSystem(body []byte) []byte {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body
	}
	raw, ok := top["messages"]
	if !ok {
		return body
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return body
	}
	changed := false
	for _, m := range msgs {
		var role string
		if json.Unmarshal(m["role"], &role) == nil && role == "developer" {
			m["role"] = json.RawMessage(`"system"`)
			changed = true
		}
	}
	if !changed {
		return body
	}
	b, err := marshalNoEscape(msgs)
	if err != nil {
		return body
	}
	top["messages"] = b
	out, err := marshalNoEscape(top)
	if err != nil {
		return body
	}
	return out
}

// marshalNoEscape is json.Marshal without rewriting <, > and & inside strings.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
