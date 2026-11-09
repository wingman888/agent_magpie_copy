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

// OpenCodeSessionHeader is the header OpenCode's gateway requires on every
// request; without it the gateway refuses the call.
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

// IsOpenCode reports whether the provider's API lives on opencode.ai or one
// of its subdomains.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		if isOpenCodeHost(p.Base(pr)) {
			return true
		}
	}
	return false
}

// IsOpenCodeBase reports whether a URL (or base URL) points at opencode.ai or
// one of its subdomains. Callers that know the actual upstream URL use this
// rather than IsOpenCode, which looks at every protocol of the provider.
func IsOpenCodeBase(base string) bool {
	return isOpenCodeHost(base)
}

func isOpenCodeHost(base string) bool {
	if base == "" {
		return false
	}
	u, err := url.Parse(base)
	if err != nil {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// OpenCodeSession picks the x-opencode-session value for a request. A
// session id the agent sent wins, taken as it is. Otherwise the value is a
// stable hash of the conversation's first user message, so every turn of
// one conversation shares it and different conversations differ.
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range agentSessionHeaders {
		// blank means absent; a value that is present is sent as it was given
		if v := in.Get(h); strings.TrimSpace(v) != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserText(body)))
	return "magpie-" + hex.EncodeToString(sum[:16])
}

// firstUserText returns the text of the first user message in a Chat
// Completions or Responses body (or any body with a "messages" array).
func firstUserText(body []byte) string {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	if len(v.Messages) > 0 {
		if t, ok := userTextIn(v.Messages); ok {
			return t
		}
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
	return ""
}

// userTextIn finds the first item with role "user" in a JSON array and
// returns its text.
func userTextIn(raw json.RawMessage) (string, bool) {
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &items) != nil {
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

// contentText is a message's content as plain text: a string as is, or the
// text parts of a content array joined in order.
func contentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
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
