package provider

// OpenCode's gateways (Go and Zen) refuse requests that carry no session
// identifier, so every request magpie sends to opencode.ai carries one.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// OpenCodeSessionHeader is the request header OpenCode's gateway reads.
const OpenCodeSessionHeader = "x-opencode-session"

// openCodeSessionSourcesL3x are the headers an agent may use to name its
// session, in priority order; the first non-empty one wins.
var openCodeSessionSourcesL3x = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// IsOpenCode reports whether a provider's base URL points at opencode.ai or
// one of its subdomains.
func (p Provider) IsOpenCode() bool {
	for _, u := range []string{p.Chat, p.Responses, p.Anthropic} {
		if u == "" {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}
		h := strings.ToLower(parsed.Hostname())
		if h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai") {
			return true
		}
	}
	return false
}

// OpenCodeSession picks the session identifier for a request to OpenCode. An
// agent's own session header is used as it is; without one, the id is derived
// from the conversation's first user message, so every turn of a conversation
// sends the same value and different conversations differ.
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range openCodeSessionSourcesL3x {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserTextL3x(body)))
	return "magpie-" + hex.EncodeToString(sum[:])[:24]
}

// firstUserTextL3x returns the text of the first user message in a Chat,
// Responses or Anthropic request body. The body's "messages" array and
// Responses' "input" (a string or an array) are both accepted. When that
// message has no text (an image alone, say), the message's raw JSON is
// returned instead, so different conversations still get different ids.
func firstUserTextL3x(body []byte) string {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(v.Input, &s) == nil {
		return s
	}
	for _, raw := range []json.RawMessage{v.Messages, v.Input} {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			continue
		}
		for _, item := range items {
			var it struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal(item, &it) != nil || it.Role != "user" {
				continue
			}
			if text := contentTextL3x(it.Content); text != "" {
				return text
			}
			return string(item)
		}
	}
	return ""
}

// contentTextL3x reads message content that is either a string or a list of
// parts with a "text" field.
func contentTextL3x(raw json.RawMessage) string {
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
