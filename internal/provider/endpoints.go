package provider

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/yetone/magpie/internal/catalog"
)

// APIs is the set of API styles the vendor's most recent model list says
// serve model, in Protocol form, or nil when the list doesn't say.
func (p Provider) APIs(model string) []Protocol {
	ms, _, ok := catalog.Live(p.ID)
	if !ok {
		return nil
	}
	for _, m := range ms {
		if m.ID != model {
			continue
		}
		var out []Protocol
		for _, a := range m.APIs {
			switch pr := Protocol(a); pr {
			case Chat, Responses, Anthropic:
				if !slices.Contains(out, pr) {
					out = append(out, pr)
				}
			}
		}
		return out
	}
	return nil
}

// declares reports whether the vendor's list says model is served over proto.
func (p Provider) declares(model string, proto Protocol) bool {
	return slices.Contains(p.APIs(model), proto)
}

// copilotAPIs maps Copilot's supported_endpoints paths to API styles. Paths
// magpie doesn't speak (websocket, embeddings, ...) are dropped; a missing
// list gives nil, which means unknown.
func copilotAPIs(endpoints []string) []string {
	var out []string
	for _, e := range endpoints {
		var a string
		switch e {
		case "/chat/completions":
			a = "chat"
		case "/responses":
			a = "responses"
		case "/v1/messages":
			a = "anthropic"
		default:
			continue
		}
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// protoOfPath is the API style a request path belongs to; it defaults to
// Anthropic, the only remaining path magpie sends upstream.
func protoOfPath(path string) Protocol {
	switch path {
	case "/chat/completions":
		return Chat
	case "/responses":
		return Responses
	}
	return Anthropic
}

// copilotTurn reads a request body in its API style and reports whether its
// last turn is the user's (not an agent's tool reply), and whether it carries
// an image.
func copilotTurn(proto Protocol, body []byte) (user, vision bool) {
	switch proto {
	case Responses:
		var v struct {
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(body, &v) != nil || len(v.Input) == 0 {
			return false, false
		}
		var s string
		if json.Unmarshal(v.Input, &s) == nil {
			return true, false
		}
		var items []struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Output  json.RawMessage `json:"output"`
		}
		if json.Unmarshal(v.Input, &items) != nil {
			return false, false
		}
		for _, it := range items {
			if hasPart(it.Content, "input_image", "image_url") || hasPart(it.Output, "input_image", "image_url") {
				vision = true
			}
		}
		if n := len(items); n > 0 {
			last := items[n-1]
			user = last.Role == "user" && last.Type != "function_call_output"
		}
		return user, vision
	case Chat, Anthropic:
		var v struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
			return false, false
		}
		for _, m := range v.Messages {
			if hasPart(m.Content, "image_url", "image") {
				vision = true
			}
		}
		last := v.Messages[len(v.Messages)-1]
		if last.Role != "user" {
			return false, vision
		}
		if proto == Anthropic {
			// a user turn that is only tool results is the agent's reply
			return !onlyToolResults(last.Content), vision
		}
		return true, vision
	}
	return false, false
}

// hasPart reports whether a content value (a string, or an array of parts)
// holds a part whose type is one of types. Parts nested in a part's own
// content (an Anthropic tool_result's images) count too, to a small depth.
func hasPart(raw json.RawMessage, types ...string) bool {
	return hasPartDepth(raw, 3, types)
}

func hasPartDepth(raw json.RawMessage, depth int, types []string) bool {
	var parts []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if depth == 0 || len(raw) == 0 || json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, p := range parts {
		if slices.Contains(types, p.Type) || hasPartDepth(p.Content, depth-1, types) {
			return true
		}
	}
	return false
}

// onlyToolResults is true for a non-empty array whose every block is a
// tool_result.
func onlyToolResults(raw json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// copilotHeadersFor sets the per-turn headers Copilot reads from a request.
func copilotHeadersFor(h http.Header, proto Protocol, body []byte) {
	user, vision := copilotTurn(proto, body)
	// a turn the user typed is billed as one; a tool's reply is not
	h.Set("X-Initiator", "agent")
	if user {
		h.Set("X-Initiator", "user")
	}
	if vision {
		h.Set("Copilot-Vision-Request", "true")
	}
}
