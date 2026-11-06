package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// OpenCodeSessionHeader is the header OpenCode's gateway insists on: a
// request without a session id is refused.
const OpenCodeSessionHeader = "x-opencode-session"

// sessionHeaders are the headers an agent may already send a session id in,
// first non-empty wins.
var sessionHeaders = []string{
	OpenCodeSessionHeader,
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCodeHost reports whether a host is opencode.ai or one of its
// subdomains. notopencode.ai is not.
func IsOpenCodeHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

// IsOpenCode reports whether the provider's base URLs point at OpenCode.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		base := p.Base(pr)
		if base == "" {
			continue
		}
		u, err := url.Parse(base)
		if err == nil && IsOpenCodeHost(u.Hostname()) {
			return true
		}
	}
	return false
}

// OpenCodeSession picks the session id for a request to OpenCode: the
// agent's own session header when it sent one, otherwise a stable id derived
// from the conversation's first user message, so every turn of a conversation
// shares it and different conversations do not. body is the request as it
// goes upstream (Chat messages, Responses input or Anthropic messages).
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserText(body)))
	return "magpie-" + hex.EncodeToString(sum[:])[:16]
}

// firstUserText returns the text of the first user message of a request, or
// "" when there is none. Responses may give input as a bare string, which is
// then the user's message itself.
func firstUserText(body []byte) string {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	list := v.Messages
	if len(list) == 0 {
		list = v.Input
	}
	var s string
	if json.Unmarshal(list, &s) == nil {
		return s
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(list, &items) != nil {
		return ""
	}
	for _, it := range items {
		var role string
		if json.Unmarshal(it["role"], &role) != nil || role != "user" {
			continue
		}
		if text := contentText(it["content"]); text != "" {
			return text
		}
	}
	return ""
}

// contentText reads message content that is either a string or a list of
// parts, joining the text parts.
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
	}
	return b.String()
}
