package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// OpenCodeSessionHeader is the header the OpenCode gateways require.
const OpenCodeSessionHeader = "x-opencode-session"

// agentSessionHeaders are the session identifiers agents already send, in
// the order they are preferred.
var agentSessionHeaders = []string{
	"x-opencode-session", "x-session-affinity", "x-session-id",
	"session_id", "session-id", "x-claude-code-session-id",
}

// OpenCodeSession picks the value of x-opencode-session for a request: the
// agent's own session id when it sent one, else a stable id derived from the
// conversation's first user message.
func OpenCodeSession(in http.Header, body []byte) string {
	for _, h := range agentSessionHeaders {
		if v := strings.TrimSpace(in.Get(h)); v != "" {
			return v
		}
	}
	sum := sha256.Sum256([]byte(firstUserText(body)))
	return "magpie-" + hex.EncodeToString(sum[:8])
}

// firstUserText is the text of the first user message of a Chat/Anthropic
// "messages" or Responses "input" body.
func firstUserText(body []byte) string {
	var req struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	items := req.Messages
	if len(items) == 0 && len(req.Input) > 0 {
		var s string
		if json.Unmarshal(req.Input, &s) == nil {
			return s
		}
		_ = json.Unmarshal(req.Input, &items)
	}
	for _, it := range items {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(it, &m) != nil || m.Role != "user" {
			continue
		}
		return contentText(m.Content)
	}
	return ""
}

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
