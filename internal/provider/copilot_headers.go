package provider

import (
	"encoding/json"
	"strings"
)

// copilotInitiator is the X-Initiator value for a request to Copilot, and
// whether the request carries an image. The body is in the API's own format,
// chosen by the path the request goes to: a turn the user typed is billed as
// one; a reply that only carries tool results is the agent's own turn.
func copilotInitiator(path string, body []byte) (initiator string, vision bool) {
	switch {
	case strings.HasSuffix(path, "/chat/completions"):
		return copilotChatInitiator(body)
	case strings.HasSuffix(path, "/responses"):
		return copilotResponsesInitiator(body)
	default:
		return copilotAnthropicInitiator(body)
	}
}

// copilotChatInitiator reads a Chat Completions body: the last message is the
// user's unless it is a tool reply.
func copilotChatInitiator(body []byte) (string, bool) {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return "agent", false
	}
	initiator := "agent"
	if n := len(v.Messages); n > 0 && v.Messages[n-1].Role == "user" {
		initiator = "user"
	}
	vision := false
	for _, m := range v.Messages {
		if rawHasBlock(m.Content, "image_url") {
			vision = true
		}
	}
	return initiator, vision
}

// copilotResponsesInitiator reads a Responses body: a string input is the
// user's; a function output (or any item not from the user) is the agent's.
func copilotResponsesInitiator(body []byte) (string, bool) {
	var v struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Input) == 0 {
		return "agent", false
	}
	var s string
	if json.Unmarshal(v.Input, &s) == nil {
		return "user", false
	}
	var items []struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(v.Input, &items) != nil {
		return "agent", false
	}
	initiator := "agent"
	if n := len(items); n > 0 && items[n-1].Type != "function_call_output" && items[n-1].Role == "user" {
		initiator = "user"
	}
	vision := false
	for _, it := range items {
		if it.Type == "input_image" || rawHasBlock(it.Content, "input_image") {
			vision = true
		}
	}
	return initiator, vision
}

// copilotAnthropicInitiator reads an Anthropic Messages body: the last message
// is the user's unless every block in it is a tool result. Text mixed in with
// tool results still counts as the user's turn.
func copilotAnthropicInitiator(body []byte) (string, bool) {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return "agent", false
	}
	initiator := "agent"
	if n := len(v.Messages); n > 0 && v.Messages[n-1].Role == "user" {
		initiator = "user"
		var blocks []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(v.Messages[n-1].Content, &blocks) == nil && len(blocks) > 0 {
			onlyResults := true
			for _, b := range blocks {
				if b.Type != "tool_result" {
					onlyResults = false
				}
			}
			if onlyResults {
				initiator = "agent"
			}
		}
	}
	vision := false
	for _, m := range v.Messages {
		if rawHasBlock(m.Content, "image") || rawNestedBlock(m.Content, "tool_result", "image") {
			vision = true
		}
	}
	return initiator, vision
}

// rawHasBlock reports whether a content array has a block of the given type.
func rawHasBlock(raw json.RawMessage, typ string) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == typ {
			return true
		}
	}
	return false
}

// rawNestedBlock reports whether a content array has a block of type outer
// whose own content array has a block of type inner (a tool result's image).
func rawNestedBlock(raw json.RawMessage, outer, inner string) bool {
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return false
	}
	for _, b := range blocks {
		if b.Type == outer && rawHasBlock(b.Content, inner) {
			return true
		}
	}
	return false
}
