package provider

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
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CopilotTokenURL exchanges a GitHub token for a short-lived Copilot session.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

const (
	copilotRelogin    = "Copilot 会话已失效，请重新登录 Copilot"
	copilotDefaultAPI = "https://api.githubcopilot.com"
	copilotEditor     = "vscode/1.99.0"
	copilotPlugin     = "copilot-chat/0.26.7"
)

// copilotCache holds the current Copilot session in memory only. The session
// token lasts minutes and is never written anywhere.
var copilotCache struct {
	mu    sync.Mutex
	oauth string
	token string
	api   string
	exp   time.Time
}

func copilotConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "github-copilot", "apps.json")
}

// readCopilot returns the GitHub login and OAuth token from Copilot's
// sign-in file: the entry whose key starts with github.com.
func readCopilot() (user, token string, ok bool) {
	b, err := os.ReadFile(copilotConfigPath())
	if err != nil {
		return "", "", false
	}
	var apps map[string]map[string]any
	if json.Unmarshal(b, &apps) != nil {
		return "", "", false
	}
	keys := make([]string, 0, len(apps))
	for k := range apps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, "github.com") {
			continue
		}
		u, _ := apps[k]["user"].(string)
		t, _ := apps[k]["oauth_token"].(string)
		if t != "" {
			return u, t, true
		}
	}
	return "", "", false
}

// copilotAccount is the Copilot account as its sign-in describes it.
func copilotAccount() (Provider, bool) {
	user, _, ok := readCopilot()
	if !ok {
		return Provider{}, false
	}
	if user == "" {
		user = "GitHub"
	}
	return Provider{ID: "copilot", Name: "GitHub Copilot", Account: "copilot", Source: "Copilot",
		User: user, Catalog: "copilot"}, true
}

// copilotSessionFor gives the request a Copilot session and the API address
// that session returned.
func copilotSessionFor(ctx context.Context, p Provider) (Provider, error) {
	_, oauth, ok := readCopilot()
	if !ok {
		return p, errors.New("Copilot 未登录，请先在编辑器或 Copilot CLI 中登录 GitHub Copilot")
	}
	c := &copilotCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.oauth != oauth || time.Now().Add(time.Minute).After(c.exp) {
		tok, api, exp, err := exchangeCopilot(ctx, oauth)
		if err != nil {
			return p, err
		}
		c.oauth, c.token, c.api, c.exp = oauth, tok, api, exp
	}
	p.Key = c.token
	p.Chat = c.api
	return p, nil
}

// exchangeCopilot trades the GitHub token for a Copilot session.
func exchangeCopilot(ctx context.Context, oauth string) (token, api string, exp time.Time, err error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("%s（%v）", copilotRelogin, err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &v) != nil || v.Token == "" {
		return "", "", time.Time{}, errors.New(copilotRelogin)
	}
	api = strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotDefaultAPI
	}
	exp = time.Now().Add(20 * time.Minute)
	if v.ExpiresAt > 0 {
		exp = time.Unix(v.ExpiresAt, 0)
	}
	return v.Token, api, exp, nil
}

// copilotList asks the account which models it may use and keeps the chat
// ones that are switched on for it.
func copilotList(ctx context.Context, p Provider) ([]catalog.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.Chat+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Editor-Version", copilotEditor)
	req.Header.Set("Editor-Plugin-Version", copilotPlugin)
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, errors.New(copilotRelogin)
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot 模型列表：%s", res.Status)
	}
	var v struct {
		Data []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled bool   `json:"model_picker_enabled"`
			Capabilities       struct {
				Type string `json:"type"`
			} `json:"capabilities"`
			Policy struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("Copilot 模型列表：%v", err)
	}
	var out []catalog.Model
	for _, m := range v.Data {
		// chat models only; internal ones are not switched on for the picker;
		// an account that has not enabled a model sees its policy say so.
		if m.ID == "" || m.ID == "auto" || m.Capabilities.Type != "chat" || !m.ModelPickerEnabled {
			continue
		}
		if m.Policy.State != "" && m.Policy.State != "enabled" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name})
	}
	if len(out) == 0 {
		return nil, errors.New("Copilot 没有返回可用的聊天模型")
	}
	return out, nil
}

// CopilotHeaders are the per-request headers Copilot wants. A turn the user
// typed is marked user-initiated; a turn that sends tool results back is
// agent-initiated and billed differently. A request with an image is marked
// as a vision request.
func CopilotHeaders(body []byte) map[string]string {
	h := map[string]string{
		"Editor-Version":         copilotEditor,
		"Editor-Plugin-Version":  copilotPlugin,
		"Copilot-Integration-Id": "vscode-chat",
		"X-Initiator":            "user",
	}
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return h
	}
	if v.Messages[len(v.Messages)-1].Role != "user" {
		h["X-Initiator"] = "agent"
	}
	for _, m := range v.Messages {
		if hasImagePart(m.Content) {
			h["Copilot-Vision-Request"] = "true"
			break
		}
	}
	return h
}

func hasImagePart(content json.RawMessage) bool {
	var parts []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return false
	}
	for _, p := range parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}

// fetchAccount refreshes the model list an account provider offers.
func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	if p.Account == "codex" {
		return p.Available(), nil // Codex keeps its own list; there is no endpoint to ask
	}
	sess, err := Session(ctx, p)
	if err != nil {
		return nil, err
	}
	ms, err := copilotList(ctx, sess)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive(p.ID, sess.Chat, ms)
}
