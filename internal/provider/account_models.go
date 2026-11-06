package provider

import (
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

// accountModels lists the models an account provider offers before any
// selection: Codex's built-in list, and for Copilot the list it last
// reported (see refreshCopilot), else the built-in list without "auto".
func (p Provider) accountModels() []catalog.Model {
	if p.Account == nil {
		return nil
	}
	switch p.Account.Agent {
	case codexID:
		return append([]catalog.Model(nil), catalog.Builtin(codexID)...)
	case copilotID:
		if live, _, ok := catalog.Live(p.ID); ok {
			return live
		}
		var out []catalog.Model
		for _, m := range catalog.Builtin(copilotID) {
			if m.ID != "auto" {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// fetchAccount refreshes an account's model list. Codex's list is built in and
// nothing is asked of the backend; Copilot is asked and the answer remembered.
func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	if p.Account == nil {
		return nil, fmt.Errorf("%s is not an account provider", p.ID)
	}
	if p.Account.Agent != copilotID {
		return p.accountModels(), nil
	}
	ms, api, err := refreshCopilot(ctx)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive(p.ID, api, ms)
}

// refreshCopilot asks the Copilot API which chat models the signed-in account
// may use. It returns the filtered list and the API base it came from.
func refreshCopilot(ctx context.Context) ([]catalog.Model, string, error) {
	l, ok := readCopilot()
	if !ok {
		return nil, "", signedOutErr(copilotID)
	}
	tok, api, err := copilotSession(ctx, l.oauth)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+"/models", nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Copilot-Integration-Id", copilotIntegration)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUA)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("copilot: model list failed: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("copilot: model list refused (%s)", APIError(b, res.Status))
	}
	var v struct {
		Data   []copilotModel `json:"data"`
		Models []copilotModel `json:"models"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, "", errors.New("copilot: unexpected model list response")
	}
	rows := v.Data
	if len(rows) == 0 {
		rows = v.Models
	}
	ms := filterCopilot(rows)
	if len(ms) == 0 {
		return nil, "", errors.New("copilot: this account has no enabled chat models")
	}
	return ms, api, nil
}

// copilotModel is the part of one Copilot model entry dial reads.
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

// filterCopilot keeps the chat models the account has enabled and that are
// not Copilot's internal helpers, in the order Copilot listed them.
func filterCopilot(rows []copilotModel) []catalog.Model {
	var out []catalog.Model
	for _, r := range rows {
		if r.ID == "" || r.Capabilities.Type != "chat" || copilotInternal(r.ID) {
			continue
		}
		if !r.ModelPickerEnabled && r.Policy.State != "enabled" {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.ID
		}
		out = append(out, catalog.Model{
			ID: r.ID, Name: name, Provider: copilotID,
			Efforts: r.Capabilities.Supports.ReasoningEffort,
		})
	}
	return out
}

// copilotInternal names the helper models Copilot uses for itself.
func copilotInternal(id string) bool {
	for _, p := range []string{"copilot-search", "exec-agent", "trajectory"} {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	for _, s := range []string{"-secondary", "-tertiary", "-4th", "-free-auto"} {
		if strings.HasSuffix(id, s) {
			return true
		}
	}
	return false
}
