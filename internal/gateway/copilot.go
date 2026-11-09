package gateway

// The Copilot account speaks Chat Completions at the API host of its session.
// Clients of any API are translated to Chat on the way out. Copilot bills a
// turn the user typed differently from a turn that only hands tool results
// back, so each request says which it is; a request carrying an image says
// so too.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yetone/dial/internal/provider"
)

// isCopilot is true for the Copilot account provider.
func isCopilot(p provider.Provider) bool {
	return p.Account != nil && p.ID == provider.CopilotID
}

// copilotBody reshapes a Chat request for Copilot: the model loses its
// copilot/ prefix, if it still has one.
func copilotBody(body []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("invalid request: %v", err)
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "copilot/")
	}
	return json.Marshal(m)
}

// copilotFlags reads a Chat request for the two things Copilot needs to be
// told: whether the turn is the user's or a tool's (initiator), and whether
// it carries an image (vision).
func copilotFlags(body []byte) (initiator string, vision bool) {
	var m struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &m)
	initiator = "user"
	if n := len(m.Messages); n > 0 && m.Messages[n-1].Role == "tool" {
		initiator = "agent"
	}
	for _, msg := range m.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(msg.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" || p.Type == "input_image" || p.Type == "image" {
				vision = true
			}
		}
	}
	return initiator, vision
}
