package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Where the account backends live. They are variables so tests can point
// them at a stand-in server.
var (
	CodexBase       = "https://chatgpt.com/backend-api/codex"
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	// CodexRefreshURL is where Codex's refresh token is traded for a new
	// access token. DIAL_CODEX_REFRESH_URL overrides the default.
	CodexRefreshURL = defaultCodexRefresh
)

const (
	defaultCodexRefresh = "https://auth.openai.com/oauth/token"
	codexClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
)

func codexRefreshURL() string {
	if CodexRefreshURL != defaultCodexRefresh {
		return CodexRefreshURL
	}
	if v := os.Getenv("DIAL_CODEX_REFRESH_URL"); v != "" {
		return v
	}
	return CodexRefreshURL
}

// ErrSignedOut marks a failure that only signing in again fixes.
var ErrSignedOut = errors.New("signed out")

func signedOut(agent, why string) error {
	cmd := "codex login"
	if agent == "copilot" {
		cmd = "sign in to GitHub Copilot in your editor or `copilot` again"
	}
	return fmt.Errorf("%s is signed out (%s): please sign in again (%s): %w", AgentName(agent), why, cmd, ErrSignedOut)
}

// Sign makes a request carry the proof the provider wants: its API key, or
// for an agent account the agent's current token. Account providers read
// the agent's files here, so a sign-out shows up as an error.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		access, acct, err := codexToken(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+access)
		if acct != "" {
			req.Header.Set("chatgpt-account-id", acct)
		}
		req.Header.Set("originator", "codex_cli_rs")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		return nil
	case "copilot":
		s, err := copilotSession(ctx)
		if err != nil {
			return err
		}
		u, err := url.Parse(strings.TrimRight(s.API, "/"))
		if err != nil || u.Host == "" {
			return fmt.Errorf("copilot sent a bad API address %q", s.API)
		}
		path := req.URL.Path
		if path == "" {
			path = "/chat/completions"
		}
		nu := *u
		nu.Path = strings.TrimRight(u.Path, "/") + path
		nu.RawQuery = req.URL.RawQuery
		req.URL = &nu
		req.Host = nu.Host
		setCopilotHeaders(req.Header)
		req.Header.Set("Authorization", "Bearer "+s.Token)
		initiator, vision := copilotInitiator(body)
		req.Header.Set("X-Initiator", initiator)
		if vision {
			req.Header.Set("Copilot-Vision-Request", "true")
		}
		return nil
	}
	return fmt.Errorf("unknown account %q", p.Account.Agent)
}

func setCopilotHeaders(h http.Header) {
	h.Set("Copilot-Integration-Id", "vscode-chat")
	h.Set("Editor-Version", "vscode/1.100.0")
	h.Set("Editor-Plugin-Version", "copilot-chat/0.27.0")
	h.Set("User-Agent", "GitHubCopilotChat/0.27.0")
	h.Set("Openai-Intent", "conversation-edits")
}

// copilotInitiator tells Copilot whether a person or the agent loop sent
// this turn, and whether it carries images.
func copilotInitiator(body []byte) (initiator string, vision bool) {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	initiator = "user"
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return
	}
	if v.Messages[len(v.Messages)-1].Role != "user" {
		initiator = "agent"
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				if p.Type == "image_url" {
					vision = true
				}
			}
		}
	}
	return
}

// Prepare shapes a request body to what the backend accepts. Ordinary
// providers get it back untouched.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil || p.Account.Agent != "codex" {
		return body
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	m["stream"] = true
	m["store"] = false
	for _, k := range []string{"max_output_tokens", "max_tokens", "max_completion_tokens", "temperature", "top_p",
		"presence_penalty", "frequency_penalty"} {
		delete(m, k)
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}}}}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- Codex token ------------------------------------------------------------

var codexMu sync.Mutex

// codexToken returns a usable access token and the account id, refreshing
// the token first when it is about to expire.
func codexToken(ctx context.Context) (access, account string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	c, ok := readCodex()
	if !ok {
		return "", "", signedOut("codex", "no ChatGPT login in ~/.codex/auth.json")
	}
	if exp, ok := jwtExpiry(c.Access); ok && time.Until(exp) < 5*time.Minute {
		if err := refreshCodex(ctx, c); err != nil {
			return "", "", err
		}
		if c, ok = readCodex(); !ok {
			return "", "", signedOut("codex", "login vanished during refresh")
		}
	}
	return c.Access, c.AccountID, nil
}

func jwtExpiry(tok string) (time.Time, bool) {
	switch v := jwtClaims(tok)["exp"].(type) {
	case float64:
		return time.Unix(int64(v), 0), true
	}
	return time.Time{}, false
}

func refreshCodex(ctx context.Context, c codexCreds) error {
	if c.Refresh == "" {
		return signedOut("codex", "its token expired and there is no refresh token")
	}
	payload, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": c.Refresh, "scope": "openid profile email"})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexRefreshURL(), bytes.NewReader(payload))
	if err != nil {
		return signedOut("codex", err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return signedOut("codex", "token refresh failed: "+err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != 200 {
		return signedOut("codex", fmt.Sprintf("token refresh failed: %s", APIError(b, res.Status)))
	}
	var tok struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &tok) != nil || tok.AccessToken == "" {
		return signedOut("codex", "token refresh returned no access token")
	}
	if err := writeCodexTokens(tok.IDToken, tok.AccessToken, tok.RefreshToken); err != nil {
		return fmt.Errorf("codex token refreshed but auth.json could not be updated: %w", err)
	}
	return nil
}

// writeCodexTokens puts new tokens back into Codex's own file, leaving
// every other field as it was and the file private.
func writeCodexTokens(id, access, refresh string) error {
	path := codexAuthPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil || root == nil {
		return fmt.Errorf("auth.json is not an object")
	}
	tokens, _ := root["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
	}
	tokens["access_token"] = access
	if id != "" {
		tokens["id_token"] = id
	}
	if refresh != "" {
		tokens["refresh_token"] = refresh
	}
	root["tokens"] = tokens
	root["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// ---- Copilot session --------------------------------------------------------

type copilotSess struct {
	Token string
	API   string
	Exp   time.Time
}

var copilotCache = struct {
	sync.Mutex
	m map[string]copilotSess
}{m: map[string]copilotSess{}}

// copilotSession trades the GitHub token for a short-lived session token,
// kept until shortly before it expires.
func copilotSession(ctx context.Context) (copilotSess, error) {
	_, oauth, ok := readCopilot()
	if !ok {
		return copilotSess{}, signedOut("copilot", "no GitHub login in github-copilot/apps.json")
	}
	key := oauth + "\n" + CopilotTokenURL
	copilotCache.Lock()
	defer copilotCache.Unlock()
	if s, ok := copilotCache.m[key]; ok && time.Until(s.Exp) > 2*time.Minute {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSess{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GithubCopilot/1.155.0")
	req.Header.Set("Editor-Version", "vscode/1.100.0")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSess{}, fmt.Errorf("copilot token exchange: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404 {
		delete(copilotCache.m, key)
		return copilotSess{}, signedOut("copilot", "GitHub refused the token: "+APIError(b, res.Status))
	}
	if res.StatusCode != 200 {
		return copilotSess{}, fmt.Errorf("copilot token exchange: %s", APIError(b, res.Status))
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSess{}, errors.New("copilot token exchange returned no token")
	}
	if v.Endpoints.API == "" {
		v.Endpoints.API = copilotPlaceholder
	}
	s := copilotSess{Token: v.Token, API: v.Endpoints.API, Exp: time.Unix(v.ExpiresAt, 0)}
	if v.ExpiresAt == 0 {
		s.Exp = time.Now().Add(3 * time.Minute)
	}
	copilotCache.m[key] = s
	return s, nil
}

// fetchCopilot asks Copilot which chat models this account can use.
func fetchCopilot(ctx context.Context) ([]catalog.Model, error) {
	s, err := copilotSession(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.API, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	setCopilotHeaders(req.Header)
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("copilot models: %s", APIError(b, res.Status))
	}
	ms, err := parseCopilotModels(b)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive("copilot", s.API, ms)
}

type copilotModel struct {
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

func internalCopilotModel(id string) bool {
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

func parseCopilotModels(b []byte) ([]catalog.Model, error) {
	var wrap struct {
		Data []copilotModel `json:"data"`
	}
	var list []copilotModel
	if err := json.Unmarshal(b, &wrap); err == nil {
		list = wrap.Data
	} else if json.Unmarshal(b, &list) != nil {
		return nil, errors.New("copilot sent an unreadable model list")
	}
	var out []catalog.Model
	for _, m := range list {
		if m.ID == "" || m.Capabilities.Type != "chat" || internalCopilotModel(m.ID) {
			continue
		}
		if !m.ModelPickerEnabled && m.Policy.State != "enabled" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name, Provider: "copilot", Efforts: m.Capabilities.Supports.ReasoningEffort})
	}
	if len(out) == 0 {
		return nil, errors.New("copilot lists no chat models this account can use")
	}
	return out, nil
}
