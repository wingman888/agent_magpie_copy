package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// sessionHeaders are the headers agents use for a conversation id, in the
// order they are tried.
var sessionHeaders = []string{"x-opencode-session", "x-session-affinity", "x-session-id", "session_id", "session-id", "x-claude-code-session-id"}

// opencodeSession is the value for x-opencode-session: the agent's own id
// when it sent one, else a stable one made from the first user message.
func opencodeSession(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	first := firstUserMessage(body)
	sum := sha256.Sum256([]byte(first))
	return "magpie-" + hex.EncodeToString(sum[:])[:32]
}

// firstUserMessage is the text of the first user message of a Chat
// (messages) or Responses (input) body, or "" when there is none.
func firstUserMessage(body []byte) string {
	var v struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	items := v.Messages
	if len(items) == 0 && len(v.Input) > 0 {
		var s string
		if json.Unmarshal(v.Input, &s) == nil {
			return s
		}
		_ = json.Unmarshal(v.Input, &items)
	}
	for _, raw := range items {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &m) != nil || m.Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			return s
		}
		var parts []struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			var b strings.Builder
			for _, p := range parts {
				b.WriteString(p.Text)
			}
			if b.Len() > 0 {
				return b.String()
			}
		}
		return string(m.Content)
	}
	return ""
}

// developerToSystem turns role "developer" messages into "system" for
// Chat Completions backends, which do not know the former. A body without
// developer messages comes back untouched.
func developerToSystem(body []byte) []byte {
	if !bytes.Contains(body, []byte("developer")) {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
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
