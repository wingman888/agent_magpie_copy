package gateway

import (
	"bytes"
	"encoding/json"
)

// developerToSystem rewrites messages whose role is "developer" to "system"
// in a Chat Completions request body. Chat backends such as DeepSeek reject
// the developer role (422), which some agents send for reasoning models. The
// content and every other field stay as they were. A body with no developer
// message is returned unchanged, byte for byte.
func developerToSystem(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"developer"`)) {
		return body
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return body
	}
	raw, ok := top["messages"]
	if !ok {
		return body
	}
	var msgs []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return body
	}
	changed := false
	for _, m := range msgs {
		if string(m["role"]) == `"developer"` {
			m["role"] = json.RawMessage(`"system"`)
			changed = true
		}
	}
	if !changed {
		return body
	}
	nm, err := json.Marshal(msgs)
	if err != nil {
		return body
	}
	top["messages"] = nm
	out, err := json.Marshal(top)
	if err != nil {
		return body
	}
	return out
}
