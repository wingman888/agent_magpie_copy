package gateway

// Requests to sign-in providers (codex, copilot). Their upstreams are the
// agents' own backends, so the body and headers are adjusted here: the
// ChatGPT backend only streams and refuses sampling fields, and Copilot wants
// to know whether a turn was typed by the user or sent back by a tool.

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// signInProtocol is the wire API a sign-in provider's backend speaks.
func signInProtocol(id string) provider.Protocol {
	if id == "copilot" {
		return provider.Chat
	}
	return provider.Responses
}

// streamOf reports whether a client body asks for a stream.
func streamOf(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	json.Unmarshal(body, &v)
	return v.Stream
}

// codexBody makes a Responses body the ChatGPT Codex backend accepts: no
// sampling or output-length fields, always streamed, never stored, and the
// input given as a list of items.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	m["stream"] = true
	m["store"] = false
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// copilotHeaders marks a Copilot request as a user turn or an agent turn
// (the last message is a tool result or a tool call being sent back), and as
// a vision request when any message carries an image.
func copilotHeaders(body []byte) map[string]string {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &v)
	h := map[string]string{"X-Initiator": "user"}
	if n := len(v.Messages); n > 0 {
		switch v.Messages[n-1].Role {
		case "tool", "assistant":
			h["X-Initiator"] = "agent"
		}
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" {
				h["Copilot-Vision-Request"] = "true"
			}
		}
	}
	return h
}

// signInExpired is true when a sign-in backend rejected the login.
func signInExpired(id string, status int) bool {
	return status == 401 || (id == "copilot" && status == 403)
}
