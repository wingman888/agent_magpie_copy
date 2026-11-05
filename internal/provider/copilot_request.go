package provider

import (
	"bytes"
	"encoding/json"
)

// userTurn reports whether a request's last turn is one the user typed,
// which Copilot bills as a user turn (X-Initiator: user). A turn that only
// carries tool results is the agent's: an Anthropic user message made of
// tool_result blocks, a Chat tool message, Responses function output. A
// Responses input given as a plain string is the user's.
func userTurn(body []byte) bool {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	if in := bytes.TrimSpace(v.Input); len(in) > 0 && string(in) != "null" {
		switch in[0] {
		case '"':
			return true
		case '[':
			var items []struct {
				Role string `json:"role"`
			}
			if json.Unmarshal(in, &items) != nil || len(items) == 0 {
				return false
			}
			return items[len(items)-1].Role == "user"
		}
		return false
	}
	if len(v.Messages) == 0 {
		return false
	}
	last := v.Messages[len(v.Messages)-1]
	if last.Role != "user" {
		return false
	}
	c := bytes.TrimSpace(last.Content)
	if len(c) == 0 || c[0] != '[' {
		return true // a string, or no content to tell by
	}
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(c, &parts) != nil || len(parts) == 0 {
		return true
	}
	for _, p := range parts {
		if p.Type != "tool_result" {
			return true
		}
	}
	return false
}

// hasImage reports whether the conversation carries an image: a Chat
// image_url part, a Responses input_image, or an Anthropic image block
// (which may also sit inside a tool result). Only the conversation is
// searched, not the tool definitions.
func hasImage(body []byte) bool {
	var v struct {
		Messages json.RawMessage `json:"messages"`
		Input    json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	for _, raw := range []json.RawMessage{v.Messages, v.Input} {
		var x any
		if len(raw) == 0 || json.Unmarshal(raw, &x) != nil {
			continue
		}
		if walkImage(x) {
			return true
		}
	}
	return false
}

func walkImage(x any) bool {
	switch x := x.(type) {
	case map[string]any:
		if t, _ := x["type"].(string); t == "image_url" || t == "input_image" || t == "image" {
			return true
		}
		for _, v := range x {
			if walkImage(v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if walkImage(v) {
				return true
			}
		}
	}
	return false
}
