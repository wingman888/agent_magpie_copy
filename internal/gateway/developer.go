package gateway

import (
	"bytes"
	"encoding/json"
)

// developerToSystem rewrites Chat Completions messages whose role is
// "developer" to "system". Chat-compatible backends (DeepSeek, among others)
// reject the role outright, so a reasoning agent such as Pi gets no reply.
// Every other field stays as it was. A body with no developer message is
// returned unchanged, byte for byte.
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
	nm, err := marshalNoEscape(msgs)
	if err != nil {
		return body
	}
	top["messages"] = nm
	out, err := marshalNoEscape(top)
	if err != nil {
		return body
	}
	return out
}

// marshalNoEscape is json.Marshal without HTML escaping, so text such as
// "<" or "&" in a message comes out as it went in.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
