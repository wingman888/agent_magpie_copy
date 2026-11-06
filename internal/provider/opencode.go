package provider

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// OpenCodeSessionHeader is the header OpenCode's gateway insists on. Requests
// without it are refused, so magpie sends one on every request to OpenCode.
const OpenCodeSessionHeader = "x-opencode-session"

// agentSessionHeaders are the session identifiers an agent may already send,
// in the order they are preferred.
var agentSessionHeaders = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCodeHost is true for opencode.ai and its subdomains.
func IsOpenCodeHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// IsOpenCode is true when one of the provider's base URLs points at OpenCode.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		if u, err := url.Parse(p.Base(pr)); err == nil && IsOpenCodeHost(u.Hostname()) {
			return true
		}
	}
	return false
}

// OpenCodeSession says what x-opencode-session to send on a request to rawURL,
// and whether to send it at all (only OpenCode hosts get it). The agent's own
// session id wins; otherwise the value is derived from the conversation's first
// user message, so later turns of one conversation keep the same id.
func OpenCodeSession(rawURL string, in http.Header, body []byte) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || !IsOpenCodeHost(u.Hostname()) {
		return "", false
	}
	for _, h := range agentSessionHeaders {
		if v := in.Get(h); strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	if text := firstUserText(body); text != "" {
		sum := sha256.Sum256([]byte(text))
		return "magpie-" + hex.EncodeToString(sum[:])[:32], true
	}
	// no user text to anchor on: the id cannot be stable, so make it unique
	var buf [16]byte
	rand.Read(buf[:])
	return "magpie-" + hex.EncodeToString(buf[:]), true
}

// firstUserText reads the first user message of a Chat (messages), Anthropic
// (messages) or Responses (input, a string or items) request body.
func firstUserText(body []byte) string {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	for _, m := range v.Messages {
		if m.Role == "user" {
			if t := contentText(m.Content); t != "" {
				return t
			}
		}
	}
	if len(v.Input) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Input, &s) == nil {
		return s
	}
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	json.Unmarshal(v.Input, &items)
	for _, it := range items {
		if it.Role == "user" {
			if t := contentText(it.Content); t != "" {
				return t
			}
		}
	}
	return ""
}

// contentText is the text of a message content: a string, or the text parts
// of an array.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Text != nil {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}
