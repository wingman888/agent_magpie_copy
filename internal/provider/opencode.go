package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// OpenCodeSessionHeader is the header OpenCode's gateway insists on. It
// refuses requests that do not name a session.
const OpenCodeSessionHeader = "x-opencode-session"

// agentSessionHeaders are the session identifiers agents already send, in the
// order they are preferred.
var agentSessionHeaders = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCode reports whether the provider's API lives on opencode.ai or one
// of its subdomains.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		if u := p.Base(pr); u != "" && IsOpenCodeHost(HostOf(u)) {
			return true
		}
	}
	return false
}

// IsOpenCodeHost reports whether a host (optionally with a port) is opencode.ai
// or a subdomain of it. notopencode.ai is not.
func IsOpenCodeHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	h = strings.TrimSuffix(h, ".")
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// ApplyOpenCodeSession sets x-opencode-session on a request bound for an
// OpenCode host. The value is the agent's own session id when it sent one;
// otherwise it is derived from the conversation's first user message, so
// every turn of one conversation carries the same id. Requests to any other
// host are left untouched.
//
// in is the agent's inbound request header (may be nil); body is the upstream
// body in the protocol the request is sent with.
func ApplyOpenCodeSession(req *http.Request, in http.Header, body []byte) {
	if req == nil || req.URL == nil || !IsOpenCodeHost(req.URL.Host) {
		return
	}
	req.Header.Set(OpenCodeSessionHeader, OpenCodeSession(in, body))
}

// OpenCodeSession picks the session id for an OpenCode request: the first
// non-empty agent session header, else a stable magpie- id.
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range agentSessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserText(body)))
	return "magpie-" + hex.EncodeToString(sum[:])[:24]
}

// firstUserText returns the text of the first user message, read from either
// a Chat-style "messages" array or a Responses-style "input" (which may be a
// bare string). It returns "" when there is no user message.
func firstUserText(body []byte) string {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{v.Messages, v.Input} {
		if t := firstUserIn(raw); t != "" {
			return t
		}
	}
	return ""
}

func firstUserIn(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	for _, it := range items {
		if it.Role != "user" {
			continue
		}
		if t := contentText(it.Content); t != "" {
			return t
		}
		// no text (say, only images): the raw content still identifies it
		return string(it.Content)
	}
	return ""
}

// contentText joins the text of a message content, which is either a string
// or an array of parts carrying "text".
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
