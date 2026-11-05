package provider

import (
	"encoding/json"
)

// Copilot reads three things from each request it is sent: whether a person
// typed the last turn (X-Initiator: user, billed as one turn) or an agent's
// tool replied to it, whether the request carries an image, and, from its
// model list, which endpoints a model is served on.

// copilotUserTurn is true when the last turn of a request body was typed by
// the user. Chat, Anthropic Messages and Responses bodies are all understood.
// A turn that only carries tool results is the agent's, not the user's.
func copilotUserTurn(body []byte) bool {
	var v struct {
		Input    json.RawMessage `json:"input"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	if len(v.Messages) == 0 {
		return responsesUserTurn(v.Input)
	}
	last := v.Messages[len(v.Messages)-1]
	if last.Role != "user" {
		return false
	}
	return !onlyToolResults(last.Content)
}

// responsesUserTurn: a bare string input is the user's; an array input is the
// user's when its last item is a user message (a function output is not).
func responsesUserTurn(input json.RawMessage) bool {
	if len(input) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(input, &s) == nil {
		return true
	}
	var items []struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(input, &items) != nil || len(items) == 0 {
		return false
	}
	return items[len(items)-1].Role == "user"
}

// onlyToolResults is true when content is a non-empty list of Anthropic
// tool_result blocks and nothing else.
func onlyToolResults(content json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// copilotHasImage reports whether a message or input item carries an image:
// a Chat image_url part, a Responses input_image, or an Anthropic image block
// (also inside a tool result). Only the conversation is searched, never tool
// schemas, so a schema that happens to name a property "image" is not a hit.
func copilotHasImage(body []byte) bool {
	var v map[string]any
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	return hasImageBlock(v["messages"]) || hasImageBlock(v["input"])
}

func hasImageBlock(v any) bool {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if hasImageBlock(e) {
				return true
			}
		}
	case map[string]any:
		switch x["type"] {
		case "image", "image_url", "input_image":
			return true
		}
		return hasImageBlock(x["content"]) || hasImageBlock(x["output"])
	}
	return false
}

// copilotAPIs maps Copilot's supported_endpoints to magpie's API names. Paths
// it does not route (websocket, embeddings, ...) are ignored; nothing
// recognised means nil, which reads as unknown.
func copilotAPIs(endpoints []string) []string {
	var out []string
	for _, e := range endpoints {
		var api string
		switch e {
		case "/chat/completions":
			api = "chat"
		case "/responses":
			api = "responses"
		case "/v1/messages":
			api = "anthropic"
		default:
			continue
		}
		if !contains(out, api) {
			out = append(out, api)
		}
	}
	return out
}
