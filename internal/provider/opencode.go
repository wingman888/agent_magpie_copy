package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// OpenCode's gateway (opencode.ai, Go and Zen alike) refuses requests that do
// not name their session, so every request sent to it carries a session id.

// OpenCodeSessionHeader is the header the OpenCode gateway reads.
const OpenCodeSessionHeader = "x-opencode-session"

// openCodeSessionSources are the headers an agent may already set to name its
// session, in the order magpie prefers them.
var openCodeSessionSources = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCodeHost reports whether a host is opencode.ai or one of its subdomains.
func IsOpenCodeHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// IsOpenCode reports whether the provider's base URLs point at OpenCode.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		if u := p.Base(pr); u != "" && IsOpenCodeHost(HostOf(u)) {
			return true
		}
	}
	return false
}

// OpenCodeSession names the session of a request headed to OpenCode. A
// session id the agent sent wins; otherwise it is derived from the first user
// message of the conversation, so later turns of the same conversation keep
// the same id and different conversations get different ones. body is the
// request as it is sent upstream (Chat messages or Responses input).
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range openCodeSessionSources {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserText(body)))
	return "magpie-" + hex.EncodeToString(sum[:12])
}

// firstUserText returns the text of the first user message in a Chat
// Completions/Anthropic-style "messages" list or a Responses "input".
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
	if len(v.Input) > 0 {
		var s string
		if json.Unmarshal(v.Input, &s) == nil {
			return s
		}
		return firstUserIn(v.Input)
	}
	return ""
}

func firstUserIn(raw json.RawMessage) string {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	for _, it := range items {
		if it.Role == "user" {
			return contentText(it.Content)
		}
	}
	return ""
}

// contentText reads message content that is either a plain string or a list
// of parts, joining the text of the parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}
