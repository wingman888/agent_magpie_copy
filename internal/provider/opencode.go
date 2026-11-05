package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// IsOpenCode reports whether any base URL of the provider is on opencode.ai
// (or a subdomain of it). Those gateways refuse requests that carry no
// session id.
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

// IsOpenCodeHost is true for opencode.ai and its subdomains, and false for
// look-alikes such as notopencode.ai.
func IsOpenCodeHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// openCodeSessionHeaders are the headers an agent may use to name its session,
// in the order they are preferred.
var openCodeSessionHeaders = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// OpenCodeSession picks the x-opencode-session value for a request: the
// agent's own session id when it sent one, otherwise a stable id derived from
// the conversation's first user message, so later turns keep the same value.
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range openCodeSessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserText(body)))
	return "magpie-" + hex.EncodeToString(sum[:8])
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// firstUserText returns the text of the first user message of a Chat
// Completions (messages) or Responses (input) body, or "" if there is none.
func firstUserText(body []byte) string {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	if len(v.Messages) > 0 {
		return firstUserIn(v.Messages)
	}
	if len(v.Input) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Input, &s) == nil {
		return s
	}
	return firstUserIn(v.Input)
}

func firstUserIn(raw json.RawMessage) string {
	var items []chatMessage
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	for _, m := range items {
		if m.Role == "user" {
			return contentText(m.Content)
		}
	}
	return ""
}

// contentText reads message content that is either a string or a list of
// parts carrying "text".
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
		b.WriteByte('\n')
	}
	return b.String()
}
