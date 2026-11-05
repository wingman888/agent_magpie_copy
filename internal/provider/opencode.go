package provider

// OpenCode's gateway (opencode.ai, Go and Zen) refuses requests that carry no
// session identifier, so magpie adds x-opencode-session to every request it
// sends there: the agent's own session id when it sends one, otherwise a
// stable id derived from the conversation's first user message.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// OpenCodeSessionHeader is the header OpenCode's gateway reads.
const OpenCodeSessionHeader = "x-opencode-session"

// sessionHeaders are the agent-supplied session ids, in order of preference.
var sessionHeaders = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCodeHost reports whether a host is opencode.ai or one of its subdomains.
func IsOpenCodeHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// IsOpenCode reports whether the provider's base URL points at OpenCode.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		u := p.Base(pr)
		if u == "" {
			continue
		}
		if parsed, err := url.Parse(u); err == nil && IsOpenCodeHost(parsed.Hostname()) {
			return true
		}
	}
	return false
}

// OpenCodeSession picks the session id for a request to OpenCode: the first
// non-empty session header the agent sent, else a magpie-derived id from the
// conversation's first user message in body (Chat messages, Responses input,
// or Anthropic messages).
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	text, raw := firstUser(body)
	seed := []byte(text)
	if text == "" {
		// No text to key on (images, tool results, no user item): hash the raw
		// JSON so different conversations still get different ids.
		seed = append([]byte("raw:"), raw...)
	}
	sum := sha256.Sum256(seed)
	return "magpie-" + hex.EncodeToString(sum[:])[:16]
}

// firstUser returns the text of the first user turn in a request body. When
// that text is empty it returns the raw JSON of the user turn instead, or of
// the whole messages/input when there is no user turn.
func firstUser(body []byte) (string, []byte) {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return "", body
	}
	list := v.Messages
	if len(v.Input) > 0 {
		var s string
		if json.Unmarshal(v.Input, &s) == nil {
			if s != "" {
				return s, nil
			}
			return "", v.Input
		}
		list = v.Input
	}
	var items []json.RawMessage
	json.Unmarshal(list, &items)
	if t, r, ok := userTurn(items); ok {
		if t != "" {
			return t, nil
		}
		return "", r
	}
	if len(list) == 0 {
		return "", body
	}
	return "", list
}

func userTurn(items []json.RawMessage) (string, json.RawMessage, bool) {
	for _, raw := range items {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &m) == nil && m.Role == "user" {
			return contentText(m.Content), raw, true
		}
	}
	return "", nil, false
}

// contentText flattens message content: a plain string, or the text parts of
// an array.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
