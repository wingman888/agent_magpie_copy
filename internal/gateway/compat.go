package gateway

import (
	"bytes"
	"encoding/json"

	"github.com/yetone/magpie/internal/provider"
)

// developerToSystem rewrites messages whose role is "developer" to "system"
// in a Chat Completions body. Many Chat backends (DeepSeek and others) reject
// the developer role with a 422, while reasoning clients such as Pi send it.
// Only Chat upstreams are touched; a body with no developer message is
// returned as-is, byte for byte.
func developerToSystem(to provider.Protocol, body []byte) []byte {
	if to != provider.Chat || !bytes.Contains(body, []byte(`"developer"`)) {
		return body
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return body
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(top["messages"], &msgs) != nil {
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
	raw, err := marshalPlain(msgs)
	if err != nil {
		return body
	}
	top["messages"] = raw
	out, err := marshalPlain(top)
	if err != nil {
		return body
	}
	return out
}

// marshalPlain encodes JSON without escaping <, > and &, so text that is
// carried over reads the same as it was sent.
func marshalPlain(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
