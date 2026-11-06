package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CopilotTokenURL is where a GitHub OAuth token is exchanged for a short-lived
// Copilot session token. A variable so tests can point it at a stand-in.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// copilotIntegrationID identifies dial to Copilot's API; it must be non-empty.
const copilotIntegrationID = "vscode-chat"

var copilotHTTP = &http.Client{Timeout: 30 * time.Second}

// copilotState remembers the last refreshed model list, in memory only.
var copilotState struct {
	mu     sync.Mutex
	models []catalog.Model // nil until RefreshCopilot succeeds
}

// copilotModels is the list Copilot accounts offer: the refreshed one when
// there is one, else the built-in list without "auto".
func copilotModels() []catalog.Model {
	copilotState.mu.Lock()
	defer copilotState.mu.Unlock()
	if copilotState.models != nil {
		return append([]catalog.Model(nil), copilotState.models...)
	}
	var out []catalog.Model
	for _, m := range catalog.Builtin("copilot") {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}

type copilotSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

type copilotModel struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ModelPickerEnabled bool   `json:"model_picker_enabled"`
	Capabilities       struct {
		Type     string `json:"type"`
		Supports struct {
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
	Policy struct {
		State string `json:"state"`
	} `json:"policy"`
}

// RefreshCopilot asks Copilot which chat models this account can use and
// remembers the answer in memory. Only the chat models the account has
// enabled are kept, internal ones are dropped, and reasoning levels are kept.
func RefreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	cred, ok := readCopilot()
	if !ok {
		return nil, errors.New("not signed in to GitHub Copilot")
	}
	sess, err := copilotExchange(ctx, cred.OAuth)
	if err != nil {
		return nil, err
	}
	list, err := copilotList(ctx, sess)
	if err != nil {
		return nil, err
	}
	var out []catalog.Model
	for _, m := range list {
		if !copilotUsable(m) {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{
			ID:       m.ID,
			Name:     name,
			Provider: "copilot",
			Efforts:  m.Capabilities.Supports.ReasoningEffort,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("GitHub Copilot lists no chat models for this account")
	}
	copilotState.mu.Lock()
	copilotState.models = out
	copilotState.mu.Unlock()
	return append([]catalog.Model(nil), out...), nil
}

// copilotExchange trades the GitHub OAuth token for a session token.
func copilotExchange(ctx context.Context, oauth string) (copilotSession, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	resp, err := copilotHTTP.Do(req)
	if err != nil {
		return copilotSession{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return copilotSession{}, fmt.Errorf("GitHub Copilot refused the token (HTTP %d); sign in to Copilot again", resp.StatusCode)
	}
	var s copilotSession
	if err := json.Unmarshal(body, &s); err != nil {
		return copilotSession{}, fmt.Errorf("GitHub Copilot token response unreadable: %w", err)
	}
	if s.Token == "" || s.Endpoints.API == "" {
		return copilotSession{}, errors.New("GitHub Copilot token response is missing the token or API endpoint")
	}
	return s, nil
}

// copilotList fetches the models the session can use.
func copilotList(ctx context.Context, sess copilotSession) ([]copilotModel, error) {
	url := strings.TrimRight(sess.Endpoints.API, "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	req.Header.Set("Copilot-Integration-Id", copilotIntegrationID)
	req.Header.Set("Accept", "application/json")
	resp, err := copilotHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub Copilot model list failed (HTTP %d)", resp.StatusCode)
	}
	return parseCopilotModels(body)
}

// parseCopilotModels accepts {"data":[...]} or a bare array, keeping order.
func parseCopilotModels(b []byte) ([]copilotModel, error) {
	var env struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.Unmarshal(b, &env); err == nil && env.Data != nil {
		return env.Data, nil
	}
	var arr []copilotModel
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr, nil
	}
	return nil, errors.New("GitHub Copilot model list unreadable")
}

// copilotUsable applies the chat, enabled and non-internal rules.
func copilotUsable(m copilotModel) bool {
	if m.ID == "" || m.Capabilities.Type != "chat" {
		return false
	}
	if copilotInternal(m.ID) {
		return false
	}
	return m.ModelPickerEnabled || m.Policy.State == "enabled"
}

var copilotInternalPrefixes = []string{"copilot-search", "exec-agent", "trajectory"}
var copilotInternalSuffixes = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}

// copilotInternal reports whether a model id is an internal helper model.
func copilotInternal(id string) bool {
	for _, p := range copilotInternalPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	for _, s := range copilotInternalSuffixes {
		if strings.HasSuffix(id, s) {
			return true
		}
	}
	return false
}
