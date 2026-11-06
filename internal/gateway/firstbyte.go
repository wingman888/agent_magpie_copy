package gateway

// A stream the gateway may still take elsewhere is held back until the first
// content reaches the agent, so that an error before any text can be dropped
// with nothing of it sent. These are the events that tell which: a content
// delta, an error, or neither (a message start, a ping, a role, a preamble).
// They are read in the agent's own protocol, the one the bytes are written in.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

// sseFields reads one server-sent event's name and data.
func sseFields(block string) (name, data string) {
	var lines []string
	for _, line := range strings.Split(block, "\n") {
		switch {
		case strings.HasPrefix(line, "event:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			d := strings.TrimPrefix(line, "data:")
			lines = append(lines, strings.TrimPrefix(d, " "))
		}
	}
	return name, strings.Join(lines, "\n")
}

// streamLook says what one event of the agent's protocol is: content, or an
// error with the status it stands for and the message it carries.
func streamLook(proto provider.Protocol, name, data string) (content bool, status int, msg string, failed bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(data), &m); err != nil {
		if name == "error" || name == "response.failed" {
			return false, 500, data, true
		}
		return false, 0, "", false
	}
	typ, _ := m["type"].(string)
	if typ == "" {
		typ = name
	}
	if name == "error" || typ == "error" || name == "response.failed" || typ == "response.failed" || m["error"] != nil {
		e := errorOf(m)
		msg = data
		if s, ok := e["message"].(string); ok && s != "" {
			msg = s
		}
		return false, errorStatus(e), msg, true
	}
	switch proto {
	case provider.Anthropic:
		return typ == "content_block_delta", 0, "", false
	case provider.Responses:
		return strings.HasSuffix(typ, ".delta"), 0, "", false
	case provider.Chat:
		choices, _ := m["choices"].([]any)
		if len(choices) == 0 {
			return false, 0, "", false
		}
		c, _ := choices[0].(map[string]any)
		d, _ := c["delta"].(map[string]any)
		if s, _ := d["content"].(string); s != "" {
			return true, 0, "", false
		}
		_, tools := d["tool_calls"]
		_, fn := d["function_call"]
		return tools || fn, 0, "", false
	}
	return false, 0, "", false
}

// errorOf is the error object an event carries: its own, or a response's.
func errorOf(m map[string]any) map[string]any {
	if e, ok := m["error"].(map[string]any); ok {
		return e
	}
	if r, ok := m["response"].(map[string]any); ok {
		if e, ok := r["error"].(map[string]any); ok {
			return e
		}
	}
	return map[string]any{}
}

// errorStatus is the HTTP status an error event stands for, from its type or
// code, as the vendors name them. Unknown errors are server errors.
func errorStatus(e map[string]any) int {
	s := strings.ToLower(fmt.Sprint(e["type"]) + " " + fmt.Sprint(e["code"]))
	switch {
	case strings.Contains(s, "overloaded"):
		return 529
	case strings.Contains(s, "rate_limit"), strings.Contains(s, "rate limit"), strings.Contains(s, "quota"), strings.Contains(s, "too_many"):
		return 429
	case strings.Contains(s, "authentication"):
		return 401
	case strings.Contains(s, "permission"):
		return 403
	case strings.Contains(s, "not_found"):
		return 404
	case strings.Contains(s, "invalid_request"):
		return 400
	}
	return 500
}
