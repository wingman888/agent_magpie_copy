package provider

// GitHub Copilot as an account provider. The sign-in is GitHub's OAuth token,
// which the editor or Copilot CLI keeps in ~/.config/github-copilot/apps.json.
// dial reads it on every use, trades it for a short-lived Copilot session token,
// and talks to the API host the session names. Nothing is cached in dial's own
// storage.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CopilotTokenURL is GitHub's endpoint that trades a GitHub token for a Copilot
// session. A var so tests can point it at a fake.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// copilotDefaultAPI is used when a session names no API host.
const copilotDefaultAPI = "https://api.githubcopilot.com"

// ErrCopilotSignedOut means the Copilot sign-in was rejected and must be renewed.
var ErrCopilotSignedOut = errors.New("copilot: 会话已失效，请重新登录 Copilot（在编辑器或 Copilot CLI 中重新登录 GitHub）")

// copilotCreds is what dial needs from apps.json for one request.
type copilotCreds struct {
	User  string
	OAuth string
}

// copilotPath is the apps.json the editor and Copilot CLI write. XDG_CONFIG_HOME
// wins over ~/.config, as those tools do.
func copilotPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "github-copilot", "apps.json")
}

// readCopilot reads the GitHub sign-in for github.com. Nothing is cached.
func readCopilot() (copilotCreds, bool) {
	b, err := os.ReadFile(copilotPath())
	if err != nil {
		return copilotCreds{}, false
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return copilotCreds{}, false
	}
	var keys []string
	for k := range apps {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var v struct {
			User  string `json:"user"`
			OAuth string `json:"oauth_token"`
		}
		if json.Unmarshal(apps[k], &v) != nil || v.OAuth == "" {
			continue
		}
		c := copilotCreds{User: v.User, OAuth: v.OAuth}
		if c.User == "" {
			c.User = "GitHub account"
		}
		return c, true
	}
	return copilotCreds{}, false
}

// copilotSignedIn reports the GitHub account Copilot is signed in with.
func copilotSignedIn() (Account, bool) {
	c, ok := readCopilot()
	if !ok {
		return Account{}, false
	}
	return Account{User: c.User, Source: accountNames["copilot"]}, true
}

// CopilotSession trades the GitHub sign-in for a Copilot session token and the
// API host to send chat requests to. A rejected sign-in gives ErrCopilotSignedOut.
func CopilotSession(ctx context.Context) (token, api string, err error) {
	c, ok := readCopilot()
	if !ok {
		return "", "", ErrCopilotSignedOut
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+c.OAuth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("copilot: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return "", "", ErrCopilotSignedOut
	}
	if res.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("copilot: token exchange answered %s", res.Status)
	}
	var s struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return "", "", fmt.Errorf("copilot: token exchange sent no session: %w", err)
	}
	if s.Token == "" {
		return "", "", errors.New("copilot: token exchange sent no session token")
	}
	api = strings.TrimRight(strings.TrimSpace(s.Endpoints.API), "/")
	if api == "" {
		api = copilotDefaultAPI
	}
	return s.Token, api, nil
}

// copilotModel is one row of the Copilot /models list. Only the fields dial
// filters on are read.
type copilotModel struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Internal            bool   `json:"internal"`
	ModelPickerCategory string `json:"model_picker_category"`
	ModelPickerEnabled  *bool  `json:"model_picker_enabled"`
	Capabilities        struct {
		Type     string `json:"type"`
		Internal bool   `json:"internal"`
	} `json:"capabilities"`
	Policy *struct {
		State string `json:"state"`
	} `json:"policy"`
}

// copilotKeep applies the filter the account's models must pass: chat models
// only, no internal ones, none that are switched off for the account, no
// embeddings, and never "auto".
func copilotKeep(m copilotModel) bool {
	if m.ID == "" || m.ID == "auto" {
		return false
	}
	if strings.Contains(strings.ToLower(m.ID), "embedding") {
		return false
	}
	if m.Capabilities.Type != "chat" {
		return false
	}
	if m.Internal || m.Capabilities.Internal || m.ModelPickerCategory == "internal" {
		return false
	}
	if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
		return false
	}
	if m.Policy != nil && m.Policy.State != "enabled" {
		return false
	}
	return true
}

// FilterCopilotModels turns a Copilot /models reply into the chat models this
// account may use, in the server's order.
func FilterCopilotModels(b []byte) ([]catalog.Model, error) {
	var v struct {
		Data   []copilotModel `json:"data"`
		Models []copilotModel `json:"models"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("copilot: model list is not JSON: %w", err)
	}
	rows := v.Data
	if len(rows) == 0 {
		rows = v.Models
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, r := range rows {
		if !copilotKeep(r) || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		name := r.Name
		if name == "" {
			name = r.ID
		}
		out = append(out, catalog.Model{ID: r.ID, Name: name})
	}
	return out, nil
}

// fetchCopilot asks the account which models it may use and saves the list, so
// later listings show the live list instead of the built-in one.
func fetchCopilot(ctx context.Context) ([]catalog.Model, error) {
	token, api, err := CopilotSession(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, ErrCopilotSignedOut
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot: models answered %s", res.Status)
	}
	ms, err := FilterCopilotModels(b)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, errors.New("copilot: this account has no chat model it may use")
	}
	return ms, catalog.SaveLive("copilot", api, ms)
}
