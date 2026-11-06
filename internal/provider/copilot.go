package provider

import (
	"bytes"
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

// GitHubAPI is where a GitHub login is traded for a Copilot session.
// Tests point it at a local fake server.
var GitHubAPI = "https://api.github.com"

// copilotDefaultAPI is the Copilot API address used until a session names its own.
const copilotDefaultAPI = "https://api.githubcopilot.com"

// ErrCopilotRelogin is what a user sees when the Copilot session is no longer valid.
var ErrCopilotRelogin = errors.New("Copilot 会话已失效，请重新登录 Copilot（在编辑器或 Copilot CLI 中重新登录 GitHub）")

// copilotAppsPath is where Copilot keeps its GitHub login.
func copilotAppsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "github-copilot", "apps.json")
}

type copilotApp struct {
	User       string `json:"user"`
	OAuthToken string `json:"oauth_token"`
}

// copilotOAuth finds the GitHub login Copilot keeps for github.com.
func copilotOAuth() (copilotApp, bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return copilotApp{}, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return copilotApp{}, false
	}
	var keys []string
	for k := range raw {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var app copilotApp
		if json.Unmarshal(raw[k], &app) == nil && app.OAuthToken != "" {
			return app, true
		}
	}
	return copilotApp{}, false
}

// copilotLogin describes the Copilot login: the GitHub login name.
func copilotLogin() (AccountInfo, bool) {
	app, ok := copilotOAuth()
	if !ok {
		return AccountInfo{}, false
	}
	return AccountInfo{User: app.User, Source: "Copilot"}, true
}

type copilotSess struct {
	token string
	api   string
}

// copilotSession trades the GitHub login for a short-lived Copilot session
// token, and takes the API address that session names.
func copilotSession(ctx context.Context) (copilotSess, error) {
	app, ok := copilotOAuth()
	if !ok {
		return copilotSess{}, ErrCopilotRelogin
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, GitHubAPI+"/copilot_internal/v2/token", nil)
	if err != nil {
		return copilotSess{}, err
	}
	req.Header.Set("Authorization", "token "+app.OAuthToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSess{}, fmt.Errorf("reaching GitHub for a Copilot session: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return copilotSess{}, ErrCopilotRelogin
	}
	if res.StatusCode != http.StatusOK {
		return copilotSess{}, fmt.Errorf("GitHub Copilot session: %s", res.Status)
	}
	var v struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSess{}, ErrCopilotRelogin
	}
	api := strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotDefaultAPI
	}
	return copilotSess{token: v.Token, api: api}, nil
}

// fetchCopilotModels lists the models this account may use in chat. Only
// chat models the account has enabled are kept; Copilot's internal models,
// embedding models and "auto" are dropped.
func fetchCopilotModels(ctx context.Context, s copilotSess) ([]catalog.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching GitHub Copilot: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, ErrCopilotRelogin
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub Copilot models: %s", res.Status)
	}
	var v struct {
		Data []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled *bool  `json:"model_picker_enabled"`
			Capabilities       struct {
				Type string `json:"type"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("GitHub Copilot models: not a model list")
	}
	var out []catalog.Model
	for _, m := range v.Data {
		if m.ID == "" || m.ID == "auto" || strings.Contains(m.ID, "embed") {
			continue
		}
		if m.Capabilities.Type != "chat" {
			continue
		}
		if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
			continue // internal: not offered in the picker
		}
		if m.Policy != nil && m.Policy.State != "" && m.Policy.State != "enabled" {
			continue // the account has not enabled it
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name})
	}
	if len(out) == 0 {
		return nil, errors.New("GitHub Copilot lists no chat models for this account")
	}
	return out, nil
}

// refreshCopilot fetches the account's live model list and remembers it.
func refreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	s, err := copilotSession(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := fetchCopilotModels(ctx, s)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive("copilot", s.api, ms)
}

// copilotTurnHeaders marks a Copilot request as a user turn or an agent turn
// (a tool result going back to the model), which Copilot bills differently,
// and flags requests that carry images.
func copilotTurnHeaders(body []byte) map[string]string {
	var v struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	json.Unmarshal(body, &v)
	initiator := "user"
	if n := len(v.Messages); n > 0 {
		if v.Messages[n-1].Role != "user" {
			initiator = "agent"
		}
	} else if len(v.Input) > 0 && v.Input[0] == '[' {
		var items []struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(v.Input, &items) == nil && len(items) > 0 && items[len(items)-1].Role != "user" {
			initiator = "agent"
		}
	}
	out := map[string]string{"X-Initiator": initiator}
	if bytes.Contains(body, []byte(`"image_url"`)) || bytes.Contains(body, []byte(`"input_image"`)) {
		out["Copilot-Vision-Request"] = "true"
	}
	return out
}
