package provider

// Copilot-specific upstream details: the headers the Copilot API expects, how
// a turn is billed (a turn the user typed versus a turn that only returns tool
// results), the vision flag, the live model list, and the re-login hint when
// the session is no longer accepted.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// The repo has no other Copilot client to copy headers from, so these are the
// values the VS Code Copilot Chat plugin usually sends.
const (
	copilotEditorVersion = "vscode/1.99.3"
	copilotPluginVersion = "copilot-chat/0.26.7"
	copilotIntegration   = "vscode-chat"
)

// copilotHeaders are the headers every Copilot API call carries, along with
// the session token.
func copilotHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":          "Bearer " + token,
		"Editor-Version":         copilotEditorVersion,
		"Editor-Plugin-Version":  copilotPluginVersion,
		"Copilot-Integration-Id": copilotIntegration,
	}
}

// CheckStatus turns an upstream status into an error when the provider is a
// Copilot login whose session is no longer accepted. Other providers and
// statuses pass.
func CheckStatus(p Provider, status int) error {
	if !p.IsAccount() || p.ID != CopilotID {
		return nil
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return errCopilotOut
	}
	return nil
}

// RequestHeaders returns the per-request headers a Copilot upstream call needs
// beyond the session: X-Initiator marks an agent turn (the last message only
// returns tool output) versus a user turn, and Copilot-Vision-Request marks a
// request that carries images. Other providers get none.
func RequestHeaders(p Provider, to Protocol, body []byte) map[string]string {
	if !p.IsAccount() || p.ID != CopilotID {
		return nil
	}
	h := map[string]string{"X-Initiator": "user"}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return h
	}
	if copilotAgentTurn(m) {
		h["X-Initiator"] = "agent"
	}
	if copilotHasImage(m) {
		h["Copilot-Vision-Request"] = "true"
	}
	return h
}

// copilotAgentTurn reports whether the request's last item is a tool result,
// in Chat (role "tool"), Anthropic (a tool_result block in a user message) or
// Responses (a function_call_output item).
func copilotAgentTurn(m map[string]any) bool {
	if list, ok := m["messages"].([]any); ok && len(list) > 0 {
		last, _ := list[len(list)-1].(map[string]any)
		role, _ := last["role"].(string)
		if role == "tool" {
			return true
		}
		return role == "user" && copilotHasBlock(last["content"], "tool_result")
	}
	if list, ok := m["input"].([]any); ok && len(list) > 0 {
		last, _ := list[len(list)-1].(map[string]any)
		t, _ := last["type"].(string)
		return t == "function_call_output"
	}
	return false
}

// copilotHasBlock reports whether a content list holds a block of the type.
func copilotHasBlock(content any, typ string) bool {
	list, _ := content.([]any)
	for _, c := range list {
		if b, ok := c.(map[string]any); ok {
			if t, _ := b["type"].(string); t == typ {
				return true
			}
		}
	}
	return false
}

// copilotHasImage reports whether any part of the request is an image.
func copilotHasImage(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if t, _ := x["type"].(string); t == "image_url" || t == "input_image" {
			return true
		}
		for _, c := range x {
			if copilotHasImage(c) {
				return true
			}
		}
	case []any:
		for _, c := range x {
			if copilotHasImage(c) {
				return true
			}
		}
	}
	return false
}

// copilotModel is one row of the Copilot API's model list.
type copilotModel struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Internal     bool           `json:"internal"`
	Picker       *bool          `json:"model_picker_enabled"`
	Policy       *copilotPolicy `json:"policy"`
	Capabilities struct {
		Type string `json:"type"`
	} `json:"capabilities"`
}

type copilotPolicy struct {
	State string `json:"state"`
}

// copilotChatModels keeps the rows the account can use for chat: chat-type
// models, not embeddings, not internal, enabled in the picker and by policy,
// and never "auto".
func copilotChatModels(rows []copilotModel) []catalog.Model {
	seen := map[string]bool{}
	var out []catalog.Model
	for _, r := range rows {
		id := strings.TrimSpace(r.ID)
		if id == "" || id == "auto" || seen[id] {
			continue
		}
		if r.Capabilities.Type != "chat" || strings.Contains(id, "embed") || r.Internal {
			continue
		}
		if r.Picker != nil && !*r.Picker {
			continue
		}
		if r.Policy != nil && r.Policy.State != "" && r.Policy.State != "enabled" {
			continue
		}
		seen[id] = true
		name := r.Name
		if name == "" {
			name = id
		}
		out = append(out, catalog.Model{ID: id, Name: name})
	}
	return out
}

// copilotListModels asks the Copilot API for the models the session may use.
func copilotListModels(ctx context.Context, base, token string) ([]catalog.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range copilotHeaders(token) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, errCopilotOut
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot models: %s", res.Status)
	}
	var v struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&v); err != nil {
		return nil, errors.New("Copilot models: not a model list")
	}
	return copilotChatModels(v.Data), nil
}

// fetchCopilot refreshes the Copilot model list from the signed-in account
// and remembers it. An empty answer is an error, so the built-in list stays.
func fetchCopilot(ctx context.Context) ([]catalog.Model, error) {
	_, oauth, ok := readCopilot()
	if !ok {
		return nil, errCopilotOut
	}
	s, err := copilotSessionOf(ctx, oauth)
	if err != nil {
		return nil, err
	}
	base := s.Endpoints.API
	if base == "" {
		base = copilotBase
	}
	ms, err := copilotListModels(ctx, base, s.Token)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, errors.New("Copilot 账号没有可用的聊天模型")
	}
	return ms, catalog.SaveLive(CopilotID, base, ms)
}
