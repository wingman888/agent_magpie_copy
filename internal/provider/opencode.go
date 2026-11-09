package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// OpenCode's gateway (opencode.ai, Go and Zen) refuses requests that do not
// name the session they belong to. These are the headers an agent may already
// send with its session id, in the order magpie prefers them.
var openCodeSessionHeaders = []string{
	"x-opencode-session",
	"x-session-affinity",
	"x-session-id",
	"session_id",
	"session-id",
	"x-claude-code-session-id",
}

// OpenCodeSessionHeader is the request header OpenCode's gateway reads.
const OpenCodeSessionHeader = "x-opencode-session"

// OpenCodeHost reports whether a host is opencode.ai or one of its subdomains.
// notopencode.ai is not.
func OpenCodeHost(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return h == "opencode.ai" || strings.HasSuffix(h, ".opencode.ai")
}

// OpenCodeURL is true when a URL points at opencode.ai or a subdomain.
func OpenCodeURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && OpenCodeHost(parsed.Hostname())
}

// IsOpenCode is true when one of the provider's base URLs is on opencode.ai.
func (p Provider) IsOpenCode() bool {
	for _, pr := range Protocols {
		if u := p.Base(pr); u != "" && OpenCodeURL(u) {
			return true
		}
	}
	return false
}

// OpenCodeSession is the value for x-opencode-session on a request to
// OpenCode. The agent's own session id wins; without one, a stable id comes
// from the conversation's first user message, so every turn of a conversation
// names the same session and different conversations do not share one.
// in is the inbound request's header, body the request as sent.
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range openCodeSessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserKey(body)))
	return "magpie-" + hex.EncodeToString(sum[:16])
}

// firstUserKey is what identifies a conversation: the text of its first user
// message. A first message with no text (only images, say) is identified by
// its own JSON instead, so two such conversations do not collide. When there
// is no user message at all, the canonical JSON of messages/input is used.
func firstUserKey(body []byte) string {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	for _, list := range []json.RawMessage{v.Messages, v.Input} {
		if len(list) == 0 {
			continue
		}
		if t, item, ok := userItem(list); ok {
			if t != "" {
				return t
			}
			return "raw\x00" + canonicalJSON(item)
		}
	}
	for _, list := range []json.RawMessage{v.Messages, v.Input} {
		if len(list) > 0 {
			return "raw\x00" + canonicalJSON(list)
		}
	}
	return ""
}

// userItem finds the first user message in a list of messages (a bare string
// input is a user message). It returns the message's text and its raw JSON.
func userItem(raw json.RawMessage) (string, json.RawMessage, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, raw, true
	}
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	var all []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || json.Unmarshal(raw, &all) != nil || len(all) != len(items) {
		return "", nil, false
	}
	for i, it := range items {
		if it.Role == "user" {
			return contentText(it.Content), all[i], true
		}
	}
	return "", nil, false
}

// canonicalJSON re-encodes JSON with sorted object keys and no whitespace, so
// the same message hashes the same however the agent formatted it.
func canonicalJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// contentText is a message's content: a string, or a list of parts whose
// text is joined.
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
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}
