package provider

// The model list of a Copilot account. dial asks Copilot which chat models
// the signed-in account may use, keeps the ones worth offering, and remembers
// the result in the catalog cache (ids, names and reasoning levels only; no
// token is ever written). Until the list has been refreshed, the built-in
// Copilot list stands in, without "auto".

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/catalog"
)

// copilotInternalPrefixes and copilotInternalSuffixes mark helper models that
// Copilot uses for itself; they are not for a user's picker.
var (
	copilotInternalPrefixes = []string{"copilot-search", "exec-agent", "trajectory"}
	copilotInternalSuffixes = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}
)

func copilotInternalModel(id string) bool {
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

// copilotWireModel is one entry of GET <endpoints.api>/models.
type copilotWireModel struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ModelPickerEnabled bool   `json:"model_picker_enabled"`
	Policy             struct {
		State string `json:"state"`
	} `json:"policy"`
	Capabilities struct {
		Type     string `json:"type"`
		Supports struct {
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
}

// copilotChatModels keeps, in the order Copilot returned them, the chat models
// the account has enabled and that are not internal helpers.
func copilotChatModels(body []byte) ([]catalog.Model, error) {
	var v struct {
		Data []copilotWireModel `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("copilot: model list is not JSON: %w", err)
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range v.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		if m.Capabilities.Type != "chat" {
			continue
		}
		if copilotInternalModel(id) {
			continue
		}
		if !m.ModelPickerEnabled && m.Policy.State != "enabled" {
			continue
		}
		seen[id] = true
		name := m.Name
		if name == "" {
			name = id
		}
		var efforts []string
		if len(m.Capabilities.Supports.ReasoningEffort) > 0 {
			efforts = append(efforts, m.Capabilities.Supports.ReasoningEffort...)
		}
		out = append(out, catalog.Model{ID: id, Name: name, Efforts: efforts})
	}
	return out, nil
}

// setCopilotHeaders labels a request to the Copilot API the way the editor
// plugin does, with the session token as the bearer credential.
func setCopilotHeaders(h http.Header, tok string) {
	h.Set("Authorization", "Bearer "+tok)
	h.Set("Copilot-Integration-Id", copilotIntegration)
	h.Set("Editor-Version", copilotEditor)
	h.Set("Editor-Plugin-Version", copilotPlugin)
	h.Set("User-Agent", copilotUA)
}

// RefreshModels asks Copilot which chat models the signed-in account can use,
// remembers the list, and returns it. It errors when no usable chat model is
// left.
func RefreshModels(ctx context.Context, p Provider) ([]catalog.Model, error) {
	if p.Account == nil || p.Account.Agent != "copilot" {
		return nil, fmt.Errorf("%s: only a Copilot account has a refreshable model list", p.Name)
	}
	c, ok := copilotLogin()
	if !ok {
		return nil, fmt.Errorf("%w: copilot — sign in to GitHub Copilot again", ErrSignedOut)
	}
	tok, api, err := copilotSession(ctx, c.OAuth)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	setCopilotHeaders(req.Header, tok)
	req.Header.Set("Accept", "application/json")
	res, err := accountHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: list models: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		// The cached session is no good any more; do not reuse it.
		copilotMu.Lock()
		delete(copilotSessions, c.OAuth)
		copilotMu.Unlock()
		return nil, fmt.Errorf("%w: copilot — GitHub rejected the sign-in; sign in to Copilot again", ErrSignedOut)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("copilot: list models: %s", APIError(b, res.Status))
	}
	models, err := copilotChatModels(b)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, errors.New("copilot: this account has no chat models enabled")
	}
	if err := catalog.SaveLive("copilot", api, models); err != nil {
		return nil, fmt.Errorf("copilot: remember model list: %w", err)
	}
	return models, nil
}

// copilotAvailable is the Copilot account's model list: the last refresh when
// there is one, else the built-in list without "auto".
func copilotAvailable() []catalog.Model {
	if live, _, ok := catalog.Live("copilot"); ok {
		return live
	}
	var out []catalog.Model
	for _, m := range catalog.Builtin("copilot") {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}
