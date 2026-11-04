package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/yetone/dial/internal/catalog"
)

// accountAvailable is what an account provider can serve: the list last
// fetched from the agent's backend, else the built-in list.
func accountAvailable(p Provider) []catalog.Model {
	if live, _, ok := catalog.Live(p.ID); ok {
		return live
	}
	return builtinAccountModels(p.ID)
}

// builtinAccountModels is the list used before any refresh. "auto" is left
// out: it is a Copilot routing choice, not a model.
func builtinAccountModels(id string) []catalog.Model {
	var out []catalog.Model
	for _, m := range catalog.Builtin(id) {
		if m.ID == "auto" {
			continue
		}
		m.Provider = id
		out = append(out, m)
	}
	return out
}

// fetchAccount refreshes the model list of an account provider and remembers it.
func fetchAccount(ctx context.Context, p Provider) ([]catalog.Model, error) {
	switch p.Account.Agent {
	case "codex":
		// Codex's list is built in, so nothing is fetched.
		ms := builtinAccountModels("codex")
		return ms, catalog.SaveLive(p.ID, CodexBase, ms)
	case "copilot":
		ms, api, err := fetchCopilotModels(ctx)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive(p.ID, api, ms)
	}
	return nil, fmt.Errorf("%s has no model list to fetch", p.Name)
}

// copilotModel is one entry of Copilot's model list. Only the fields dial uses are read.
type copilotModel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Capabilities struct {
		Type     string `json:"type"`
		Supports struct {
			ReasoningEffort []string `json:"reasoning_effort"`
		} `json:"supports"`
	} `json:"capabilities"`
	ModelPickerEnabled bool `json:"model_picker_enabled"`
	Policy             struct {
		State string `json:"state"`
	} `json:"policy"`
}

// fetchCopilotModels asks Copilot which chat models this account can use.
// It returns the models and the API address they were listed from.
func fetchCopilotModels(ctx context.Context) ([]catalog.Model, string, error) {
	c, ok := loadCopilot()
	if !ok {
		return nil, "", errCopilotSignedOut
	}
	s, err := copilotSessionFor(ctx, c.oauth)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, "", err
	}
	setCopilotHeaders(req.Header, s.token)
	req.Header.Set("Accept", "application/json")
	res, err := copilotHTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("copilot: listing models: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("copilot: listing models: %s", APIError(b, res.Status))
	}
	var body struct {
		Data []copilotModel `json:"data"`
	}
	if json.Unmarshal(b, &body) != nil {
		return nil, "", fmt.Errorf("copilot: unreadable model list")
	}
	var out []catalog.Model
	for _, m := range body.Data {
		if !keepCopilotModel(m) {
			continue
		}
		name := m.Name
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
		return nil, "", fmt.Errorf("copilot lists no chat models for this account")
	}
	return out, s.api, nil
}

// keepCopilotModel keeps chat models that are enabled for the account and are
// not internal helper models.
func keepCopilotModel(m copilotModel) bool {
	if m.Capabilities.Type != "chat" {
		return false
	}
	if internalCopilotModel(m.ID) {
		return false
	}
	return m.ModelPickerEnabled || m.Policy.State == "enabled"
}

var (
	internalPrefixes = []string{"copilot-search", "exec-agent", "trajectory"}
	internalSuffixes = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}
)

// internalCopilotModel recognises Copilot's internal helper models by id.
func internalCopilotModel(id string) bool {
	for _, p := range internalPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	for _, s := range internalSuffixes {
		if strings.HasSuffix(id, s) {
			return true
		}
	}
	return false
}
