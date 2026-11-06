package gateway

import (
	"bytes"
	"encoding/json"
)

// developerToSystem turns "developer" messages into "system" ones for a Chat
// Completions backend, which rejects the role. Only the role changes; a body
// with no developer message is returned as it came, byte for byte.
func developerToSystem(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	msgs, _ := m["messages"].([]any)
	changed := false
	for _, x := range msgs {
		if mm, ok := x.(map[string]any); ok && mm["role"] == "developer" {
			mm["role"] = "system"
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
