package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// OpenCodeSessionHeader is the header OpenCode's gateways require on every
// request; they refuse requests that carry no session identifier.
const OpenCodeSessionHeader = "x-opencode-session"

// openCodeSessionSources are the headers an agent may already carry a session
// identifier in, in priority order. The first non-empty one wins.
var openCodeSessionSources = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// OpenCodeHost reports whether a host name is opencode.ai or a subdomain of it.
// A port or trailing dot is ignored; notopencode.ai is not a match.
func OpenCodeHost(host string) bool {
	h := strings.ToLower(host)
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// OpenCodeSession picks the x-opencode-session value for one conversation.
// It is the agent's own session identifier when it sent one; otherwise a
// magpie- value derived from the conversation's first user message, which
// stays the same across turns and differs between conversations.
func OpenCodeSession(in http.Header, firstUser string) string {
	for _, k := range openCodeSessionSources {
		if v := strings.TrimSpace(in.Get(k)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUser))
	return "magpie-" + hex.EncodeToString(sum[:])[:24]
}

// FirstUserText returns the text of the first user message in a request body.
// It reads Chat Completions and Anthropic messages, and the Responses input
// (a plain string, or an array of items).
func FirstUserText(body []byte) string {
	var v struct {
		Messages []openCodeItem  `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	for _, m := range v.Messages {
		if m.Role == "user" {
			return openCodeText(m.Content)
		}
	}
	if len(v.Input) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Input, &s) == nil {
		return s
	}
	var items []openCodeItem
	if json.Unmarshal(v.Input, &items) == nil {
		for _, it := range items {
			if it.Role == "user" {
				return openCodeText(it.Content)
			}
		}
	}
	return ""
}

// SetOpenCodeSession sets x-opencode-session on req when it is headed to an
// OpenCode host. Requests to any other host are left alone.
func SetOpenCodeSession(req *http.Request, sid string) {
	if req == nil || sid == "" || !OpenCodeHost(req.URL.Host) {
		return
	}
	req.Header.Set(OpenCodeSessionHeader, sid)
}

type openCodeItem struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// openCodeText reads message content: a string, or an array whose text parts
// are joined.
func openCodeText(raw json.RawMessage) string {
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
