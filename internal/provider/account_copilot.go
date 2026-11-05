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

// copilotAPI is the default Copilot endpoint; a session names its own, which wins.
const copilotAPI = "https://api.githubcopilot.com"

// CopilotTokenURL exchanges a GitHub token for a short-lived Copilot session.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

// copilotBuiltin is the model list used until the user refreshes it. It never
// holds "auto".
var copilotBuiltin = []string{"gpt-4.1", "gpt-5-mini", "claude-sonnet-4.5", "gemini-2.5-pro"}

const copilotRelogin = "请重新登录 Copilot（在编辑器或 Copilot CLI 中重新登录 GitHub）"

var errCopilotOut = errors.New(copilotRelogin)

var copilotMu sync.Mutex

// copilotSessions holds session tokens for the life of the process only.
// They are short-lived and are never written anywhere.
var copilotSessions = map[string]copilotSession{}

type copilotSession struct {
	token   string
	api     string
	expires time.Time
}

// copilotLogin is the GitHub login the Copilot apps keep in apps.json.
type copilotLogin struct {
	user  string
	oauth string
}

func copilotConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = homeJoin(".config")
	}
	return filepath.Join(dir, "github-copilot", "apps.json")
}

// readCopilot finds the github.com entry in apps.json.
func readCopilot() (copilotLogin, error) {
	b, err := os.ReadFile(copilotConfigPath())
	if err != nil {
		return copilotLogin{}, errNotSignedIn
	}
	var apps map[string]json.RawMessage
	if json.Unmarshal(b, &apps) != nil {
		return copilotLogin{}, errNotSignedIn
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
		var v struct {
			User  string `json:"user"`
			OAuth string `json:"oauth_token"`
		}
		if json.Unmarshal(apps[k], &v) == nil && v.OAuth != "" {
			return copilotLogin{user: v.User, oauth: v.OAuth}, nil
		}
	}
	return copilotLogin{}, errNotSignedIn
}

func copilotProvider() *Provider {
	l, err := readCopilot()
	if err != nil {
		return nil
	}
	return &Provider{
		ID: CopilotID, Name: "Copilot", Chat: copilotAPI,
		Website: "https://github.com/features/copilot",
		Account: &Account{User: l.user, Source: "Copilot"},
	}
}

// copilotClientHeaders identify dial to Copilot the way its editor clients do.
func copilotClientHeaders() map[string]string {
	return map[string]string{
		"User-Agent":             "GitHubCopilotChat/0.26.7",
		"Editor-Version":         "vscode/1.99.3",
		"Editor-Plugin-Version":  "copilot-chat/0.26.7",
		"Copilot-Integration-Id": "vscode-chat",
	}
}

// copilotSessionFor exchanges the GitHub token for a session, reusing a live one.
func copilotSessionFor(ctx context.Context, oauth string) (copilotSession, error) {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if s, ok := copilotSessions[oauth]; ok && time.Until(s.expires) > time.Minute {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotClientHeaders() {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("Copilot: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return copilotSession{}, errCopilotOut
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil || out.Token == "" {
		return copilotSession{}, errCopilotOut
	}
	s := copilotSession{token: out.Token, api: strings.TrimRight(out.Endpoints.API, "/"), expires: time.Unix(out.ExpiresAt, 0)}
	if out.ExpiresAt == 0 {
		s.expires = time.Now().Add(20 * time.Minute)
	}
	copilotSessions[oauth] = s
	return s, nil
}

// forgetCopilotSessions drops cached sessions after Copilot refused one.
func forgetCopilotSessions() {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	clear(copilotSessions)
}

// copilotHeaders are the headers of one chat request. X-Initiator says whether
// the turn was typed by the user or sent back by a tool; Copilot bills the two
// differently. Vision requests are flagged so Copilot can route them.
func copilotHeaders(token string, body []byte) map[string]string {
	h := copilotClientHeaders()
	h["Authorization"] = "Bearer " + token
	h["openai-intent"] = "conversation-panel"
	h["X-Initiator"] = copilotInitiator(body)
	if copilotHasImage(body) {
		h["Copilot-Vision-Request"] = "true"
	}
	return h
}

func copilotInitiator(body []byte) string {
	var v struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return "user"
	}
	if last := v.Messages[len(v.Messages)-1].Role; last != "user" {
		return "agent"
	}
	return "user"
}

func copilotHasImage(body []byte) bool {
	var v struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}

// fetchCopilotModels asks the account which models it may use, and keeps
// only the chat models it has switched on.
func fetchCopilotModels(ctx context.Context) (string, []catalog.Model, error) {
	l, err := readCopilot()
	if err != nil {
		return "", nil, errors.New("Copilot 未登录，请先登录 Copilot")
	}
	s, err := copilotSessionFor(ctx, l.oauth)
	if err != nil {
		return "", nil, err
	}
	base := s.api
	if base == "" {
		base = copilotAPI
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotClientHeaders() {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("Copilot: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		forgetCopilotSessions()
		return "", nil, errCopilotOut
	}
	if res.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("Copilot: model list: %s", res.Status)
	}
	var out struct {
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
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&out); err != nil {
		return "", nil, fmt.Errorf("Copilot: model list: %v", err)
	}
	var ms []catalog.Model
	for _, m := range out.Data {
		switch {
		case m.ID == "" || m.ID == "auto":
			continue
		case m.Capabilities.Type != "chat": // embeddings and the like
			continue
		case m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled: // internal, not for pickers
			continue
		case m.Policy != nil && m.Policy.State != "" && m.Policy.State != "enabled": // switched off for this account
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		ms = append(ms, catalog.Model{ID: m.ID, Name: name, Provider: "github-copilot"})
	}
	return base, ms, nil
}
