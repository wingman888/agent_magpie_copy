package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// OpenCodeSessionHeader is the header OpenCode's gateway insists on: requests
// without it are refused.
const OpenCodeSessionHeader = "x-opencode-session"

// sessionHeaders are the session identifiers an agent may already send, in
// the order they are preferred.
var sessionHeaders = []string{
	OpenCodeSessionHeader,
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCodeHost reports whether a host is opencode.ai or one of its subdomains.
func IsOpenCodeHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// IsOpenCode reports whether the provider's base URLs point at OpenCode.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		u := p.Base(pr)
		if u == "" {
			continue
		}
		if pu, err := url.Parse(u); err == nil && IsOpenCodeHost(pu.Hostname()) {
			return true
		}
	}
	return false
}

// SetOpenCodeHeaders adds x-opencode-session to a request bound for OpenCode.
// Any other host gets nothing. in is the agent's request headers (may be nil)
// and body is the request body being sent, whose first user message stands in
// for a session when the agent sent none.
func SetOpenCodeHeaders(req *http.Request, in http.Header, body []byte) {
	if req.URL == nil || !IsOpenCodeHost(req.URL.Hostname()) {
		return
	}
	req.Header.Set(OpenCodeSessionHeader, OpenCodeSession(in, body))
}

// OpenCodeSession picks the session identifier for an OpenCode request: the
// agent's own session header if it sent one, otherwise a stable "magpie-"
// value derived from the conversation's first user message.
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range sessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	text, _ := firstUserText(body)
	sum := sha256.Sum256([]byte(text))
	return "magpie-" + hex.EncodeToString(sum[:])[:32]
}

// firstUserText finds the first user message in a Chat Completions body
// (messages) or a Responses body (input, a string or a list of items).
func firstUserText(body []byte) (string, bool) {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return "", false
	}
	if len(v.Messages) > 0 {
		if t, ok := userTextIn(v.Messages); ok {
			return t, true
		}
	}
	if len(v.Input) > 0 {
		var s string
		if json.Unmarshal(v.Input, &s) == nil {
			return s, true
		}
		if t, ok := userTextIn(v.Input); ok {
			return t, true
		}
	}
	return "", false
}

// userTextIn returns the content of the first item whose role is user.
func userTextIn(list json.RawMessage) (string, bool) {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(list, &items) != nil {
		return "", false
	}
	for _, it := range items {
		if it.Role == "user" {
			return contentText(it.Content), true
		}
	}
	return "", false
}

// contentText flattens message content: a plain string, or a list of parts
// whose text is joined. Content with no text falls back to its raw JSON.
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
		if b.Len() > 0 {
			return b.String()
		}
	}
	return string(bytes.TrimSpace(raw))
}
