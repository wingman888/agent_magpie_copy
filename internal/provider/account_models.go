package provider

// Model lists and selections for account providers. What an account offers
// comes from the built-in list (Codex) or from the login itself, refreshed
// on demand (Copilot). dial keeps only the user's selection and that fetched
// list in its config: never an address, key or token.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/dial/internal/catalog"
)

// isAccountID reports whether id names an account provider.
func isAccountID(id string) bool { return accountSpecFor(id) != nil }

// accountSpecFor returns the account spec with the given id, or nil.
func accountSpecFor(id string) *accountSpec {
	for i := range accountSpecs {
		if accountSpecs[i].id == id {
			return &accountSpecs[i]
		}
	}
	return nil
}

// hasAddress is true for a self-built provider, which has an address.
func hasAddress(p Provider) bool {
	return p.Chat != "" || p.Responses != "" || p.Anthropic != ""
}

// storedHasAddress reports whether the config holds a self-built provider
// with this id, which takes precedence over the account.
func storedHasAddress(id string) bool {
	rec, ok := findStored(load().Providers, id)
	return ok && hasAddress(rec)
}

// Owner names whose login a provider uses: the signed-in user for an account
// provider, the provider's own name otherwise.
func (p Provider) Owner() string {
	if p.Account != nil && p.Account.User != "" {
		return p.Account.User
	}
	return p.Name
}

// accountModels is what an account provider can serve: the login's last
// fetched list when there is one, else the built-in list.
func (p Provider) accountModels() []catalog.Model {
	if p.Account == nil {
		return nil
	}
	var builtin []catalog.Model
	if spec := accountSpecFor(p.Account.Agent); spec != nil {
		builtin = spec.builtin()
	}
	if len(p.Listed) == 0 {
		return builtin
	}
	known := make(map[string]catalog.Model, len(builtin))
	for _, m := range builtin {
		known[m.ID] = m
	}
	out := make([]catalog.Model, 0, len(p.Listed))
	for _, id := range p.Listed {
		m := catalog.Model{ID: id, Name: id, Provider: p.Catalog}
		if k, ok := known[id]; ok {
			m.Name, m.Efforts = k.Name, k.Efforts
		}
		if e := p.Efforts[id]; len(e) > 0 {
			m.Efforts = e
		}
		out = append(out, m)
	}
	return out
}

// fetchAccount refreshes an account provider's model list. Codex answers from
// its built-in list without the network; Copilot asks the account and keeps
// the answer.
func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	switch p.Account.Agent {
	case "codex":
		return p.accountModels(), nil
	case "copilot":
		return refreshCopilot(ctx)
	}
	return nil, fmt.Errorf("%s: cannot refresh the models of this account", p.Name)
}

// saveSelection stores the models chosen for an account provider. Its
// address, key and every other field are ignored; only the selection is kept.
func saveSelection(p Provider) error {
	spec := accountSpecFor(p.ID)
	if _, ok := spec.detect(); !ok {
		return fmt.Errorf("%s is not signed in: log in with the agent first (dial keeps no login of its own)", spec.name)
	}
	picks := cleanList(p.Models)
	return updateRecord(p.ID, func(r *Provider) { r.Models = picks })
}

// updateRecord changes the config record for id, creating an id-only record
// when there is none, and stores the file.
func updateRecord(id string, fn func(*Provider)) error {
	f := load()
	i := -1
	for j := range f.Providers {
		if f.Providers[j].ID == id {
			i = j
			break
		}
	}
	if i < 0 {
		f.Providers = append(f.Providers, Provider{ID: id})
		i = len(f.Providers) - 1
	}
	fn(&f.Providers[i])
	return store(f)
}

// ---- Copilot ---------------------------------------------------------------

// copilotModel is the part of one entry of Copilot's model list dial reads.
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

// copilotInternalPrefixes and copilotInternalSuffixes mark Copilot's internal
// and helper models, which are not for users to pick.
var (
	copilotInternalPrefixes = []string{"copilot-search", "exec-agent", "trajectory"}
	copilotInternalSuffixes = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}
)

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

// copilotChatModel keeps chat models the account has enabled, excluding
// internal ones.
func copilotChatModel(m copilotModel) bool {
	if m.Capabilities.Type != "chat" || copilotInternal(m.ID) {
		return false
	}
	return m.ModelPickerEnabled || m.Policy.State == "enabled"
}

// filterCopilotModels keeps, in the order given, the models that pass
// copilotChatModel, and returns their reasoning levels by id.
func filterCopilotModels(list []copilotModel) ([]catalog.Model, map[string][]string) {
	var out []catalog.Model
	efforts := map[string][]string{}
	seen := map[string]bool{}
	for _, m := range list {
		if m.ID == "" || seen[m.ID] || !copilotChatModel(m) {
			continue
		}
		seen[m.ID] = true
		name := m.Name
		if name == "" {
			name = m.ID
		}
		cm := catalog.Model{ID: m.ID, Name: name}
		if e := m.Capabilities.Supports.ReasoningEffort; len(e) > 0 {
			cm.Efforts = slices.Clone(e)
			efforts[m.ID] = cm.Efforts
		}
		out = append(out, cm)
	}
	return out, efforts
}

// parseCopilotModels reads the model list, which is either {"data":[...]} or
// a bare array.
func parseCopilotModels(b []byte) ([]copilotModel, error) {
	var env struct {
		Data []copilotModel `json:"data"`
	}
	if json.Unmarshal(b, &env) == nil && env.Data != nil {
		return env.Data, nil
	}
	var list []copilotModel
	if json.Unmarshal(b, &list) == nil && list != nil {
		return list, nil
	}
	return nil, errors.New("copilot: 模型清单响应无效")
}

// copilotModelList asks the account which chat models it may use. The GitHub
// token is exchanged for a session first, as for a chat request.
func copilotModelList(ctx context.Context, oauth string) ([]catalog.Model, map[string][]string, error) {
	session, api, err := copilotSessionFor(ctx, oauth)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(api, "/")+"/models", nil)
	if err != nil {
		return nil, nil, err
	}
	h := req.Header
	h.Set("Authorization", "Bearer "+session)
	h.Set("Accept", "application/json")
	h.Set("Copilot-Integration-Id", copilotIntegration)
	h.Set("Editor-Version", copilotEditor)
	h.Set("Editor-Plugin-Version", copilotPlugin)
	h.Set("User-Agent", copilotUA)
	res, err := signClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("copilot: 读取模型清单失败：%w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, nil, errors.New(copilotRelogin)
	}
	if res.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("copilot: 读取模型清单失败（%s）：%s", res.Status, APIError(b, res.Status))
	}
	list, err := parseCopilotModels(b)
	if err != nil {
		return nil, nil, err
	}
	models, efforts := filterCopilotModels(list)
	if len(models) == 0 {
		return nil, nil, errors.New("copilot: 该账号没有可用的聊天模型")
	}
	return models, efforts, nil
}

// refreshCopilot fetches the account's models and keeps them in the config:
// the list, the reasoning levels, and the user's selection narrowed to what
// is still offered (an empty selection means the whole list).
func refreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	l, ok := copilotLogin()
	if !ok {
		return nil, errors.New(copilotRelogin)
	}
	models, efforts, err := copilotModelList(ctx, l.token)
	if err != nil {
		return nil, err
	}
	ids := modelIDs(models)
	err = updateRecord("copilot", func(r *Provider) {
		var picks []string
		for _, id := range r.Models {
			if slices.Contains(ids, id) {
				picks = append(picks, id)
			}
		}
		r.Models = picks
		r.Listed = ids
		r.Efforts = efforts
	})
	if err != nil {
		return nil, fmt.Errorf("copilot: 无法保存模型清单：%w", err)
	}
	return models, nil
}
