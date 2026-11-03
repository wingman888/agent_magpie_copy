package gateway

import (
	"bytes"
	"encoding/json"
)

// developerToSystem rewrites messages whose role is "developer" to "system"
// in a Chat Completions request. Chat backends other than OpenAI's own (DeepSeek
// and the like) reject the developer role outright. Every other field is kept;
// a body with no developer message is returned byte for byte.
func developerToSystem(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"developer"`)) {
		return body
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body
	}
	var msgs []json.RawMessage
	if json.Unmarshal(top["messages"], &msgs) != nil {
		return body
	}
	changed := false
	for i, m := range msgs {
		var fields map[string]json.RawMessage
		if json.Unmarshal(m, &fields) != nil {
			continue
		}
		var role string
		if json.Unmarshal(fields["role"], &role) != nil || role != "developer" {
			continue
		}
		fields["role"] = json.RawMessage(`"system"`)
		b, err := marshalNoHTML(fields)
		if err != nil {
			return body
		}
		msgs[i] = b
		changed = true
	}
	if !changed {
		return body
	}
	mb, err := marshalNoHTML(msgs)
	if err != nil {
		return body
	}
	top["messages"] = mb
	out, err := marshalNoHTML(top)
	if err != nil {
		return body
	}
	return out
}

// marshalNoHTML encodes v as JSON without escaping <, > and & in the text.
func marshalNoHTML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
