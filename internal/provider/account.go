package provider

// Accounts: agents the user has signed in to, offered as providers.
//
// A Codex CLI login (ChatGPT) or a Copilot login is a subscription with
// models behind it. dial reads the credentials the agent itself keeps on
// disk, so every other agent can use those models through the gateway.
// Nothing is stored twice: sign out of the agent and the provider is gone.
//
// Claude Code is deliberately absent: Anthropic's terms keep a Claude
// subscription's OAuth token for Claude Code alone. Excluded says so when
// that sign-in exists, so its absence here is not a mystery.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account is the signed-in agent behind a provider.
type Account struct {
	Agent string `json:"agent"`          // the agent's id: codex, copilot
	User  string `json:"user"`           // who is signed in: an email, a GitHub login
	Plan  string `json:"plan,omitempty"` // the subscription, when the agent says

	// Stream is set when the backend only streams; dial then translates
	// a non-streaming request instead of relaying it.
	Stream bool `json:"-"`

	sign   func(ctx context.Context, req *http.Request, body []byte) error
	body   func(body []byte) []byte // request tweaks the backend insists on
	models func() []catalog.Model
	fetch  func(ctx context.Context) ([]catalog.Model, error)
}

// Sign authenticates a request to the provider, refreshing what needs it.
// Plain providers get their key; accounts get the agent's tokens.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account != nil && p.Account.sign != nil {
		return p.Account.sign(ctx, req, body)
	}
	for k, v := range AuthHeaders(p, proto) {
		req.Header.Set(k, v)
	}
	return nil
}

// Prepare adjusts a request body the way the backend wants it.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.body != nil {
		return p.Account.body(body)
	}
	return body
}

// Exclusion is a sign-in dial found but will not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

var (
	excludedMu   sync.Mutex
	excludedAt   time.Time
	excludedList []Exclusion
)

// Excluded lists the sign-ins dial leaves alone, and why. Claude Code's
// credentials live in the macOS Keychain or ~/.claude/.credentials.json;
// only their presence is checked, never their contents.
func Excluded() []Exclusion {
	excludedMu.Lock()
	defer excludedMu.Unlock()
	if time.Since(excludedAt) < time.Minute {
		return excludedList
	}
	excludedAt, excludedList = time.Now(), nil
	if claudeSignedIn() {
		excludedList = append(excludedList, Exclusion{Agent: "claude",
			Why: "Anthropic's terms keep a Claude subscription for Claude Code itself, so dial does not share it with other agents. Use an Anthropic API key as a provider instead."})
	}
	return excludedList
}

func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil {
		return true
	}
	if runtime.GOOS == "darwin" {
		// listing the item needs no Keychain prompt; reading it would
		return exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials").Run() == nil
	}
	return false
}

// Accounts lists the signed-in agents as providers.
func Accounts() []Provider {
	home, _ := os.UserHomeDir()
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(home, ".config")
	}
	var out []Provider
	if p, ok := codexAccount(home); ok {
		out = append(out, p)
	}
	if p, ok := copilotAccount(cfg); ok {
		out = append(out, p)
	}
	return out
}

func readJSON(path string, v any) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

// jwtClaims decodes the payload of a JWT without checking it; the
// tokens are the user's own, only their expiry and subject matter here.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func claimString(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[k]
	}
	s, _ := v.(string)
	return s
}

// ---- Codex CLI: a ChatGPT account ----------------------------------------------

const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann" // Codex CLI's own OAuth client
	codexTokenURL = "https://auth.openai.com/oauth/token"
)

// CodexBase is where a ChatGPT account's Codex requests go; a var so tests
// can point it elsewhere.
var CodexBase = "https://chatgpt.com/backend-api/codex"

var codexMu sync.Mutex

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

func codexAccount(home string) (Provider, bool) {
	path := filepath.Join(home, ".codex", "auth.json")
	var a codexAuth
	if !readJSON(path, &a) || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
		return Provider{}, false
	}
	id := jwtClaims(a.Tokens.IDToken)
	acct := &Account{Agent: "codex", Stream: true,
		User: claimString(id, "email"), Plan: claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")}
	if acct.User == "" {
		acct.User = "ChatGPT"
	}
	acct.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
		tok, accountID, err := codexToken(ctx, path)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if accountID != "" {
			req.Header.Set("chatgpt-account-id", accountID)
		}
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "dial")
		return nil
	}
	acct.body = codexBody
	acct.models = catalog.Codex
	acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
	return Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Responses: CodexBase, Website: "https://chatgpt.com/codex", Account: acct}, true
}

// codexToken returns a usable access token, refreshing it through OpenAI
// when it is about to expire. A refresh rotates the tokens, so the new
// ones go back into auth.json for Codex CLI to find.
func codexToken(ctx context.Context, path string) (tok, accountID string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	var a codexAuth
	if !readJSON(path, &a) || a.Tokens.AccessToken == "" {
		return "", "", errors.New("Codex is signed out; run codex login")
	}
	accountID = a.Tokens.AccountID
	if accountID == "" {
		accountID = claimString(jwtClaims(a.Tokens.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(a.Tokens.AccessToken)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
		return a.Tokens.AccessToken, accountID, nil
	}
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": a.Tokens.RefreshToken, "scope": "openid profile email"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", errors.New("Codex token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return "", "", errors.New("Codex is signed out (token refresh failed); run codex login")
	}
	// keep every other field of the file as Codex CLI wrote it
	var raw map[string]any
	if readJSON(path, &raw) {
		toks, _ := raw["tokens"].(map[string]any)
		if toks == nil {
			toks = map[string]any{}
		}
		toks["access_token"] = fresh.AccessToken
		if fresh.IDToken != "" {
			toks["id_token"] = fresh.IDToken
		}
		if fresh.RefreshToken != "" {
			toks["refresh_token"] = fresh.RefreshToken
		}
		raw["tokens"] = toks
		raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
		if out, err := json.MarshalIndent(raw, "", "  "); err == nil {
			os.WriteFile(path, append(out, '\n'), 0o600)
		}
	}
	return fresh.AccessToken, accountID, nil
}

// codexBody makes a Responses request acceptable to the ChatGPT backend:
// it streams only, keeps nothing, wants a list of input items, and
// rejects the sampling knobs.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "temperature", "top_p", "previous_response_id", "user", "safety_identifier", "service_tier"} {
		delete(m, k)
	}
	m["store"] = false
	m["stream"] = true
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

// ---- Copilot: a GitHub account -------------------------------------------------

// CopilotTokenURL trades the GitHub OAuth token for a short-lived Copilot
// session token; a var so tests can point it elsewhere.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

const copilotBase = "https://api.githubcopilot.com"

var copilotHeaders = map[string]string{
	"Editor-Version":         "vscode/1.104.0",
	"Editor-Plugin-Version":  "copilot-chat/0.31.0",
	"Copilot-Integration-Id": "vscode-chat",
	"User-Agent":             "GitHubCopilotChat/0.31.0",
}

type copilotSession struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API string `json:"api"`
	} `json:"endpoints"`
}

var (
	copilotMu       sync.Mutex
	copilotSessions = map[string]copilotSession{} // by GitHub token
)

type copilotApp struct {
	User  string `json:"user"`
	Token string `json:"oauth_token"`
}

// copilotLogin finds the GitHub token Copilot's editors and CLI keep.
func copilotLogin(cfg string) (copilotApp, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		var apps map[string]copilotApp
		if !readJSON(filepath.Join(cfg, "github-copilot", name), &apps) {
			continue
		}
		var keys []string
		for k := range apps {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.HasPrefix(k, "github.com") && apps[k].Token != "" {
				return apps[k], true
			}
		}
	}
	return copilotApp{}, false
}

func copilotAccount(cfg string) (Provider, bool) {
	app, ok := copilotLogin(cfg)
	if !ok {
		return Provider{}, false
	}
	acct := &Account{Agent: "copilot", User: app.User}
	if acct.User == "" {
		acct.User = "GitHub"
	}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		s, err := copilotToken(ctx, app.Token)
		if err != nil {
			return err
		}
		if s.Endpoints.API != "" {
			if u, err := url.Parse(s.Endpoints.API + req.URL.Path); err == nil {
				req.URL, req.Host = u, u.Host
			}
		}
		req.Header.Set("Authorization", "Bearer "+s.Token)
		for k, v := range copilotHeaders {
			req.Header.Set(k, v)
		}
		req.Header.Set("Openai-Intent", "conversation-panel")
		// a turn the user typed is billed as one; a tool's reply is not
		req.Header.Set("X-Initiator", "agent")
		if lastRole(body) == "user" {
			req.Header.Set("X-Initiator", "user")
		}
		if bytes.Contains(body, []byte(`"image_url"`)) {
			req.Header.Set("Copilot-Vision-Request", "true")
		}
		return nil
	}
	acct.models = func() []catalog.Model {
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
	acct.fetch = func(ctx context.Context) ([]catalog.Model, error) {
		ms, err := copilotModels(ctx, app.Token)
		if err != nil {
			return nil, err
		}
		return ms, catalog.SaveLive("copilot", copilotBase, ms)
	}
	return Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Chat: copilotBase, Website: "https://github.com/features/copilot", Account: acct}, true
}

// lastRole is the role of the last message in a chat request.
func lastRole(body []byte) string {
	var v struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return ""
	}
	return v.Messages[len(v.Messages)-1].Role
}

func copilotToken(ctx context.Context, github string) (copilotSession, error) {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if s, ok := copilotSessions[github]; ok && time.Until(time.Unix(s.ExpiresAt, 0)) > 2*time.Minute {
		return s, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+github)
	req.Header.Set("Accept", "application/json")
	for k, v := range copilotHeaders {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, errors.New("Copilot sign-in: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var s copilotSession
	if res.StatusCode != 200 || json.Unmarshal(b, &s) != nil || s.Token == "" {
		return copilotSession{}, errors.New("Copilot is signed out (" + APIError(b, res.Status) + "); sign in to Copilot again")
	}
	copilotSessions[github] = s
	return s, nil
}

// internal is a Copilot model id nobody picks by hand.
var copilotInternal = regexp.MustCompile(`^(copilot-search|exec-agent|trajectory)|-(secondary|tertiary|4th|free-auto)$`)

// copilotModels asks Copilot which chat models this account may use.
func copilotModels(ctx context.Context, github string) ([]catalog.Model, error) {
	s, err := copilotToken(ctx, github)
	if err != nil {
		return nil, err
	}
	base := s.Endpoints.API
	if base == "" {
		base = copilotBase
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	for k, v := range copilotHeaders {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var v struct {
		Data []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Picker       bool   `json:"model_picker_enabled"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					Efforts []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &v) != nil {
		return nil, errors.New("Copilot models: " + APIError(b, res.Status))
	}
	var out []catalog.Model
	for _, m := range v.Data {
		if m.Capabilities.Type != "chat" || copilotInternal.MatchString(m.ID) {
			continue
		}
		// a model the account has not enabled at github.com answers 403
		if !m.Picker && (m.Policy == nil || m.Policy.State != "enabled") {
			continue
		}
		out = append(out, catalog.Model{ID: m.ID, Name: m.Name, Efforts: m.Capabilities.Supports.Efforts})
	}
	if len(out) == 0 {
		return nil, errors.New("Copilot lists no chat model for this account")
	}
	return out, nil
}
