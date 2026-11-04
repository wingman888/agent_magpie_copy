package provider

// The Copilot account: the GitHub login the editor or Copilot CLI keeps in
// $XDG_CONFIG_HOME/github-copilot. dial reads the GitHub token from there,
// trades it for a short-lived Copilot session token, and caches that session
// until shortly before it expires.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CopilotTokenURL is where GitHub tokens are exchanged for Copilot session
// tokens. A variable so tests can point it at a stand-in.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

const (
	copilotIntegrationID = "vscode-chat"
	copilotEditorVersion = "vscode/1.99.3"
	copilotPluginVersion = "copilot-chat/0.26.7"
	copilotUserAgent     = "GitHubCopilotChat/0.26.7"
	copilotDefaultAPI    = "https://api.githubcopilot.com"
)

// errCopilotSignedOut is the error to show when the GitHub login is gone.
var errCopilotSignedOut = errors.New("copilot: 已登出，请重新登录（在编辑器或 Copilot CLI 中登录 GitHub）")

func copilotConfigDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "github-copilot")
}

// copilotCreds is the GitHub login: a user name and an OAuth token.
type copilotCreds struct {
	user, oauth string
}

// readCopilot finds the first github.com entry with a token in apps.json, then
// in hosts.json.
func readCopilot() (copilotCreds, bool) {
	dir := copilotConfigDir()
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(b, &entries) != nil {
			continue
		}
		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !strings.HasPrefix(k, "github.com") {
				continue
			}
			var e struct {
				User  string `json:"user"`
				OAuth string `json:"oauth_token"`
			}
			if json.Unmarshal(entries[k], &e) != nil || e.OAuth == "" {
				continue
			}
			user := e.User
			if user == "" {
				user = "GitHub"
			}
			return copilotCreds{user: user, oauth: e.OAuth}, true
		}
	}
	return copilotCreds{}, false
}

// copilotSession is a Copilot session token and the API it is good for.
type copilotSession struct {
	token, api string
	expires    time.Time
}

var copilotMu sync.Mutex
var copilotSessions = map[string]copilotSession{}

// copilotSessionFor returns a session for the GitHub token, exchanging again
// when the cached one is within two minutes of expiring.
func copilotSessionFor(ctx context.Context, oauth string) (copilotSession, error) {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	key := CopilotTokenURL + "\x00" + oauth
	if s, ok := copilotSessions[key]; ok && time.Until(s.expires) > 2*time.Minute {
		return s, nil
	}
	s, err := exchangeCopilot(ctx, oauth)
	if err != nil {
		return copilotSession{}, err
	}
	copilotSessions[key] = s
	return s, nil
}

func exchangeCopilot(ctx context.Context, oauth string) (copilotSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	setCopilotIdentity(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("copilot token exchange: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return copilotSession{}, fmt.Errorf("%w（GitHub 令牌被拒绝：%s）", errCopilotSignedOut, res.Status)
	}
	if res.StatusCode != http.StatusOK {
		return copilotSession{}, fmt.Errorf("copilot token exchange: %s", res.Status)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return copilotSession{}, fmt.Errorf("copilot token exchange: %w", err)
	}
	if out.Token == "" {
		return copilotSession{}, errors.New("copilot token exchange: no token in reply")
	}
	api := strings.TrimRight(out.Endpoints.API, "/")
	if api == "" {
		api = copilotDefaultAPI
	}
	exp := time.Now().Add(20 * time.Minute)
	if out.ExpiresAt > 0 {
		exp = time.Unix(out.ExpiresAt, 0)
	}
	return copilotSession{token: out.Token, api: api, expires: exp}, nil
}

// setCopilotIdentity adds the headers the Copilot API expects from an editor.
func setCopilotIdentity(h http.Header) {
	h.Set("Copilot-Integration-Id", copilotIntegrationID)
	h.Set("Editor-Version", copilotEditorVersion)
	h.Set("Editor-Plugin-Version", copilotPluginVersion)
	h.Set("User-Agent", copilotUserAgent)
}

// signCopilot sends a chat request to the session's API with the session
// token. The body decides whether the request counts as user-initiated, and
// whether it is a vision request.
func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	c, ok := readCopilot()
	if !ok {
		return errCopilotSignedOut
	}
	s, err := copilotSessionFor(ctx, c.oauth)
	if err != nil {
		return err
	}
	u, err := url.Parse(s.api + "/chat/completions")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+s.token)
	setCopilotIdentity(req.Header)
	req.Header.Set("Openai-Intent", "conversation-panel")
	req.Header.Set("X-Initiator", copilotInitiator(body))
	if hasImage(body) {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	return nil
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func chatMessages(body []byte) []chatMessage {
	var v struct {
		Messages []chatMessage `json:"messages"`
	}
	json.Unmarshal(body, &v)
	return v.Messages
}

// copilotInitiator is "user" when the last message is the user's, else "agent".
func copilotInitiator(body []byte) string {
	msgs := chatMessages(body)
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
		return "user"
	}
	return "agent"
}

// hasImage reports whether any message carries an image part.
func hasImage(body []byte) bool {
	for _, m := range chatMessages(body) {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			switch p.Type {
			case "image_url", "image", "input_image":
				return true
			}
		}
	}
	return false
}

// copilotModel is one entry of the Copilot models list.
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

var copilotInternalPrefixes = []string{"copilot-search", "exec-agent", "trajectory"}
var copilotInternalSuffixes = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}

// keepCopilot keeps chat models the account has enabled, that are not
// internal helpers, and not the "auto" router.
func keepCopilot(m copilotModel) bool {
	if m.Capabilities.Type != "chat" || m.ID == "" || m.ID == "auto" {
		return false
	}
	for _, p := range copilotInternalPrefixes {
		if strings.HasPrefix(m.ID, p) {
			return false
		}
	}
	for _, s := range copilotInternalSuffixes {
		if strings.HasSuffix(m.ID, s) {
			return false
		}
	}
	return m.ModelPickerEnabled || m.Policy.State == "enabled"
}

// filterCopilot keeps the usable models in the order given.
func filterCopilot(all []copilotModel) []catalog.Model {
	var out []catalog.Model
	for _, m := range all {
		if !keepCopilot(m) {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{
			ID:      m.ID,
			Name:    name,
			Efforts: append([]string(nil), m.Capabilities.Supports.ReasoningEffort...),
		})
	}
	return out
}

// refreshCopilot asks Copilot which chat models this account can use, and
// remembers the answer.
func (p Provider) refreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	c, ok := readCopilot()
	if !ok {
		return nil, errCopilotSignedOut
	}
	s, err := copilotSessionFor(ctx, c.oauth)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	setCopilotIdentity(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot models: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w（%s）", errCopilotSignedOut, res.Status)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot models: %s", res.Status)
	}
	var v struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("copilot models: %w", err)
	}
	ms := filterCopilot(v.Data)
	if len(ms) == 0 {
		return nil, errors.New("copilot: 账号没有可用的聊天模型")
	}
	return ms, catalog.SaveLive(p.ID, s.api, ms)
}
