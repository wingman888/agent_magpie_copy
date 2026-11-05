package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// OpenCodeSessionHeader is the header the OpenCode gateway (opencode.ai)
// insists on. Requests without it are refused.
const OpenCodeSessionHeader = "x-opencode-session"

// openCodeSessionSources are the request headers an agent may already carry a
// session id in, in priority order. The first non-empty value is used as is.
var openCodeSessionSources = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCode reports whether the provider's API host is opencode.ai or one of
// its subdomains. Those hosts need OpenCodeSessionHeader.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		if u := p.Base(pr); u != "" && isOpenCodeHost(baseHostname(u)) {
			return true
		}
	}
	return false
}

// baseHostname returns the lower-cased host of a base URL without port.
func baseHostname(u string) string {
	if pu, err := url.Parse(strings.TrimSpace(u)); err == nil && pu.Hostname() != "" {
		return strings.ToLower(pu.Hostname())
	}
	return strings.ToLower(HostOf(u))
}

func isOpenCodeHost(h string) bool {
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// OpenCodeSession picks the value of OpenCodeSessionHeader for one request.
// The agent's own session header wins, taken as is. Otherwise the id is
// derived from the first user message of the conversation, so later turns of
// the same conversation keep the same id and other conversations get another.
// h may be nil; body is the JSON request body in any of magpie's client
// shapes (Chat messages, Responses input, Anthropic messages).
func OpenCodeSession(h http.Header, body []byte) string {
	for _, k := range openCodeSessionSources {
		if h != nil {
			if v := strings.TrimSpace(h.Get(k)); v != "" {
				return v
			}
		}
	}
	sum := sha256.Sum256([]byte(firstUserText(body)))
	return "magpie-" + hex.EncodeToString(sum[:])[:24]
}

// firstUserText is the text of the first user message in a request body. It
// reads "messages" (Chat, Anthropic) or "input" (Responses, where input may
// also be a bare string, which is then the user's message).
func firstUserText(body []byte) string {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	if len(v.Input) > 0 {
		var s string
		if json.Unmarshal(v.Input, &s) == nil {
			return s
		}
		if t, ok := userTextIn(v.Input); ok {
			return t
		}
	}
	if t, ok := userTextIn(v.Messages); ok {
		return t
	}
	return ""
}

// userTextIn scans a JSON array of messages or items for the first one whose
// role is "user" and returns its text.
func userTextIn(raw json.RawMessage) (string, bool) {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return "", false
	}
	for _, it := range items {
		if it.Role != "user" {
			continue
		}
		return contentText(it.Content), true
	}
	return "", false
}

// contentText flattens message content: a string, or an array of parts whose
// text is concatenated in order.
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
