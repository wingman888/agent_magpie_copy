package gateway

import (
	"bytes"
	"encoding/json"
)

// developerToSystem rewrites Chat Completions messages whose role is
// "developer" to "system". Chat-compatible backends (DeepSeek, among others)
// reject the role outright, so a reasoning agent such as Pi gets no reply.
// Only the role values change: every other byte of the body is kept as it
// was sent, including whitespace, key order, and escaping. A body with no
// developer message is returned unchanged, byte for byte.
func developerToSystem(body []byte) []byte {
	top, ok := members(body, true)
	if !ok {
		return body
	}
	var edits []span // byte ranges of "developer" role values, in document order
	for _, m := range top {
		if m.key != "messages" {
			continue
		}
		list := body[m.span.start:m.span.end]
		items, ok := members(list, false)
		if !ok {
			continue
		}
		for _, it := range items {
			msg := list[it.span.start:it.span.end]
			fields, ok := members(msg, true)
			if !ok {
				continue
			}
			for _, f := range fields {
				if f.key != "role" {
					continue
				}
				var role string
				if json.Unmarshal(msg[f.span.start:f.span.end], &role) == nil && role == "developer" {
					base := m.span.start + it.span.start
					edits = append(edits, span{base + f.span.start, base + f.span.end})
				}
			}
		}
	}
	if len(edits) == 0 {
		return body
	}
	var out bytes.Buffer
	out.Grow(len(body))
	last := 0
	for _, e := range edits {
		out.Write(body[last:e.start])
		out.WriteString(`"system"`)
		last = e.end
	}
	out.Write(body[last:])
	return out.Bytes()
}

// span is a half-open byte range [start, end) of a JSON value in its document.
type span struct{ start, end int }

// member is one entry of a JSON object (key set) or one element of a JSON
// array (key empty), with the byte range of its value.
type member struct {
	key  string
	span span
}

// members lists the entries of doc, which must be a JSON object when object
// is true and a JSON array otherwise. Spans are offsets into doc.
func members(doc []byte, object bool) ([]member, bool) {
	open := json.Delim('[')
	if object {
		open = json.Delim('{')
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	tok, err := dec.Token()
	if err != nil {
		return nil, false
	}
	if d, ok := tok.(json.Delim); !ok || d != open {
		return nil, false
	}
	var out []member
	for dec.More() {
		var key string
		if object {
			tok, err := dec.Token()
			if err != nil {
				return nil, false
			}
			k, ok := tok.(string)
			if !ok {
				return nil, false
			}
			key = k
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, false
		}
		end := int(dec.InputOffset())
		out = append(out, member{key: key, span: span{start: end - len(raw), end: end}})
	}
	return out, true
}
