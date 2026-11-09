package provider

// Account providers reuse a login the user already made in an agent: Codex
// (ChatGPT account) and Copilot (GitHub account). dial never copies those
// credentials into its own store; it reads the agent's file each time it
// needs them. The only thing dial keeps for an account provider is which of
// its models the user ticked.
//
// Claude Code is deliberately not an account provider. dial only checks that
// its credentials file exists, so it can say so in the CLI and API.

import (
	"bytes"
	"context"
	"encoding/base64"
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

// Ids of the account providers, and of the agent that is never offered.
const (
	CodexID   = "codex"
	CopilotID = "copilot"
	ClaudeID  = "claude"
)

// CodexBase is the ChatGPT Codex backend. Tests point it at a fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// Endpoints and HTTP client, overridable for tests.
var (
	CodexTokenURL   = "https://auth.openai.com/oauth/token"
	CodexClientID   = "app_EMoamEEZ73f0CkXaXp7hrann"
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
	accountHTTP     = &http.Client{Timeout: 30 * time.Second}
)

// ErrCodexSignedOut is returned when Codex's login is gone or can no longer be refreshed.
var ErrCodexSignedOut = errors.New("Codex 已登出，请运行 `codex login`")

// ErrCopilotSignIn is returned when the GitHub token no longer gets a Copilot session.
var ErrCopilotSignIn = errors.New("Copilot 会话已失效，请重新登录 Copilot")

// claudeReason is why Claude Code is never offered as a provider.
const claudeReason = "Claude Code 的登录凭据只供它自己使用，dial 不读取、不转发，因此不作为 Provider 提供"

// Account says whose login backs an account provider.
type Account struct {
	Provider string `json:"provider"` // codex or copilot
	Agent    string `json:"agent"`    // the agent the login belongs to: Codex or Copilot
	User     string `json:"user"`     // email for Codex, login name for Copilot
	Plan     string `json:"plan,omitempty"`
}

var accountNames = map[string]string{CodexID: "Codex", CopilotID: "Copilot"}

// IsAccount reports whether an id names an account provider.
func IsAccount(id string) bool { return id == CodexID || id == CopilotID }

// reservedID ids can never be a user's own provider.
func reservedID(id string) bool { return IsAccount(id) || id == ClaudeID }

// accountFor returns the login behind an account provider, or nil when the
// agent is not signed in.
func accountFor(id string) *Account {
	switch id {
	case CodexID:
		return codexAccount()
	case CopilotID:
		return copilotAccount()
	}
	return nil
}

// accountProviders lists the account providers whose agent is signed in.
// Ticked models come from the config file; a residual tick for an agent
// that is not signed in is ignored.
func accountProviders() []Provider {
	saved := load().Providers
	var out []Provider
	for _, id := range []string{CodexID, CopilotID} {
		acc := accountFor(id)
		if acc == nil {
			continue
		}
		p := Provider{ID: id, Name: accountNames[id], Account: acc}
		for _, s := range saved {
			if s.ID == id {
				p.Models = cleanList(s.Models)
			}
		}
		out = append(out, p)
	}
	return out
}

// saveAccount stores only the ticked models of an account provider.
func saveAccount(p Provider) error {
	if accountFor(p.ID) == nil {
		return fmt.Errorf("%s is not signed in; sign in with the agent first", accountNames[p.ID])
	}
	f := load()
	f.Providers = withoutID(f.Providers, p.ID)
	if models := cleanList(p.Models); len(models) > 0 {
		f.Providers = append(f.Providers, Provider{ID: p.ID, Models: models})
	}
	return store(f)
}

// deleteAccount clears the ticked models of an account provider. It never
// touches the agent's login.
func deleteAccount(f file, id string) error {
	found := false
	for _, p := range f.Providers {
		if p.ID == id {
			found = true
		}
	}
	if !found && accountFor(id) == nil {
		return fmt.Errorf("no provider %q", id)
	}
	if !found {
		return nil
	}
	f.Providers = withoutID(f.Providers, id)
	return store(f)
}

func withoutID(ps []Provider, id string) []Provider {
	keep := make([]Provider, 0, len(ps))
	for _, p := range ps {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	return keep
}

// ---- Codex ------------------------------------------------------------------

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// readCodexAuth returns the whole auth.json (kept so it can be written back
// with its other fields), the auth mode and the tokens.
func readCodexAuth() (map[string]json.RawMessage, string, codexTokens, error) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, "", codexTokens{}, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, "", codexTokens{}, err
	}
	var head struct {
		AuthMode string      `json:"auth_mode"`
		Tokens   codexTokens `json:"tokens"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return nil, "", codexTokens{}, err
	}
	return top, head.AuthMode, head.Tokens, nil
}

// codexAccount is the ChatGPT login, when Codex has one. An API-key login
// is not an account and gives nil.
func codexAccount() *Account {
	_, mode, t, err := readCodexAuth()
	if err != nil || mode != "chatgpt" || (t.AccessToken == "" && t.RefreshToken == "") {
		return nil
	}
	acc := &Account{Provider: CodexID, Agent: accountNames[CodexID], User: "ChatGPT"}
	if c, err := jwtClaims(t.IDToken); err == nil {
		if email := stringClaim(c, "email"); email != "" {
			acc.User = email
		}
		acc.Plan = codexPlan(c)
	}
	return acc
}

func codexPlan(c map[string]any) string {
	auth, _ := c["https://api.openai.com/auth"].(map[string]any)
	plan, _ := auth["chatgpt_plan_type"].(string)
	return plan
}

// CodexCredentials are what one request to the Codex backend needs.
type CodexCredentials struct {
	AccessToken string
	AccountID   string
	Email       string
	Plan        string
	Expires     time.Time
}

// CodexToken reads Codex's login for one request. When the access token
// expires within five minutes it refreshes it the way Codex does and writes
// the new tokens back into Codex's own auth.json, keeping its other fields.
func CodexToken(ctx context.Context) (CodexCredentials, error) {
	top, mode, t, err := readCodexAuth()
	if err != nil || mode != "chatgpt" || (t.AccessToken == "" && t.RefreshToken == "") {
		return CodexCredentials{}, ErrCodexSignedOut
	}
	exp, _ := jwtExpiry(t.AccessToken)
	if exp.IsZero() || time.Until(exp) < 5*time.Minute {
		if t.RefreshToken == "" {
			return CodexCredentials{}, ErrCodexSignedOut
		}
		fresh, err := refreshCodex(ctx, t.RefreshToken)
		if err != nil {
			return CodexCredentials{}, ErrCodexSignedOut
		}
		if err := saveCodexTokens(top, fresh); err != nil {
			return CodexCredentials{}, fmt.Errorf("保存刷新后的 Codex 令牌失败: %w", err)
		}
		t.AccessToken = fresh.AccessToken
		if fresh.RefreshToken != "" {
			t.RefreshToken = fresh.RefreshToken
		}
		if fresh.IDToken != "" {
			t.IDToken = fresh.IDToken
		}
		exp, _ = jwtExpiry(t.AccessToken)
	}
	c, _ := jwtClaims(t.IDToken)
	return CodexCredentials{
		AccessToken: t.AccessToken,
		AccountID:   t.AccountID,
		Email:       stringClaim(c, "email"),
		Plan:        codexPlan(c),
		Expires:     exp,
	}, nil
}

type codexRefresh struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
}

func refreshCodex(ctx context.Context, refresh string) (codexRefresh, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     CodexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refresh,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexTokenURL, bytes.NewReader(body))
	if err != nil {
		return codexRefresh{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := accountHTTP.Do(req)
	if err != nil {
		return codexRefresh{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexRefresh{}, fmt.Errorf("token refresh: %s", res.Status)
	}
	var out codexRefresh
	if err := json.Unmarshal(b, &out); err != nil {
		return codexRefresh{}, err
	}
	if out.AccessToken == "" {
		return codexRefresh{}, errors.New("token refresh: no access token")
	}
	return out, nil
}

// saveCodexTokens writes refreshed tokens into auth.json, leaving every
// other field as it was.
func saveCodexTokens(top map[string]json.RawMessage, fresh codexRefresh) error {
	tokens := map[string]json.RawMessage{}
	if raw, ok := top["tokens"]; ok {
		if err := json.Unmarshal(raw, &tokens); err != nil {
			return err
		}
	}
	set := func(key, val string) {
		if val == "" {
			return
		}
		b, _ := json.Marshal(val)
		tokens[key] = b
	}
	set("access_token", fresh.AccessToken)
	set("refresh_token", fresh.RefreshToken)
	set("id_token", fresh.IDToken)
	tb, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	top["tokens"] = tb
	top["last_refresh"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(codexAuthPath(), append(out, '\n'), 0o600)
}

// jwtClaims decodes a JWT payload. The signature is not checked; dial only
// reads display fields and expiry from tokens it was given.
func jwtClaims(tok string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil, errors.New("not a JWT")
	}
	seg := strings.TrimRight(parts[1], "=")
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(seg)
		if err != nil {
			return nil, err
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// jwtExpiry reads the exp claim of a JWT. ok is false when there is none.
func jwtExpiry(tok string) (time.Time, bool) {
	c, err := jwtClaims(tok)
	if err != nil {
		return time.Time{}, false
	}
	exp, ok := c["exp"].(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}

func stringClaim(c map[string]any, key string) string {
	s, _ := c[key].(string)
	return s
}

// ---- Copilot ----------------------------------------------------------------

func copilotAppsPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "github-copilot", "apps.json")
}

type copilotLogin struct {
	User  string
	OAuth string
}

// readCopilotLogin finds the github.com entry in the Copilot apps file.
func readCopilotLogin() (copilotLogin, bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return copilotLogin{}, false
	}
	var apps map[string]struct {
		User  string `json:"user"`
		OAuth string `json:"oauth_token"`
	}
	if json.Unmarshal(b, &apps) != nil {
		return copilotLogin{}, false
	}
	var keys []string
	for k := range apps {
		if strings.HasPrefix(k, "github.com") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if a := apps[k]; a.OAuth != "" {
			return copilotLogin{User: a.User, OAuth: a.OAuth}, true
		}
	}
	return copilotLogin{}, false
}

func copilotAccount() *Account {
	l, ok := readCopilotLogin()
	if !ok {
		return nil
	}
	user := l.User
	if user == "" {
		user = "GitHub"
	}
	return &Account{Provider: CopilotID, Agent: accountNames[CopilotID], User: user}
}

// CopilotSession is a short-lived token for the Copilot API.
type CopilotSession struct {
	Token   string
	API     string // the API base URL the session returned
	Expires time.Time
}

// CopilotToken exchanges the GitHub token from the Copilot login for a session.
func CopilotToken(ctx context.Context) (CopilotSession, error) {
	l, ok := readCopilotLogin()
	if !ok {
		return CopilotSession{}, ErrCopilotSignIn
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return CopilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+l.OAuth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := accountHTTP.Do(req)
	if err != nil {
		return CopilotSession{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound {
		return CopilotSession{}, ErrCopilotSignIn
	}
	if res.StatusCode != http.StatusOK {
		return CopilotSession{}, fmt.Errorf("copilot session: %s", res.Status)
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&v); err != nil {
		return CopilotSession{}, err
	}
	if v.Token == "" {
		return CopilotSession{}, ErrCopilotSignIn
	}
	api := strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = "https://api.githubcopilot.com"
	}
	return CopilotSession{Token: v.Token, API: api, Expires: time.Unix(v.ExpiresAt, 0)}, nil
}

// copilotModelRow is one entry of the Copilot model list.
type copilotModelRow struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	PickerEnabled *bool  `json:"model_picker_enabled"`
	Capabilities  struct {
		Type string `json:"type"`
	} `json:"capabilities"`
	Policy *struct {
		State string `json:"state"`
	} `json:"policy"`
}

// copilotChatModel keeps the models a chat client may use on this account:
// chat models only, no embeddings, no auto, no internal ones, and only those
// the account has enabled.
func copilotChatModel(r copilotModelRow) bool {
	id := strings.TrimSpace(r.ID)
	if id == "" || id == "auto" {
		return false
	}
	if strings.Contains(id, "embed") || strings.Contains(strings.ToLower(id), "internal") {
		return false
	}
	if r.Capabilities.Type != "" && r.Capabilities.Type != "chat" {
		return false
	}
	if r.PickerEnabled != nil && !*r.PickerEnabled {
		return false
	}
	if r.Policy != nil && r.Policy.State != "" && r.Policy.State != "enabled" {
		return false
	}
	return true
}

// FetchCopilotModels asks the Copilot API which models this account can use,
// filters them and saves the list for the catalog.
func FetchCopilotModels(ctx context.Context) ([]catalog.Model, error) {
	sess, err := CopilotToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sess.API+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Editor-Version", "vscode/1.99.0")
	req.Header.Set("User-Agent", "dial")
	res, err := accountHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return nil, ErrCopilotSignIn
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot models: %s", res.Status)
	}
	var v struct {
		Data []copilotModelRow `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&v); err != nil {
		return nil, fmt.Errorf("copilot models: %w", err)
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, r := range v.Data {
		if !copilotChatModel(r) || seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		name := r.Name
		if name == "" {
			name = r.ID
		}
		out = append(out, catalog.Model{ID: r.ID, Name: name})
	}
	if len(out) == 0 {
		return nil, errors.New("Copilot 账号没有可用的聊天模型")
	}
	if err := catalog.SaveLive(CopilotID, sess.API, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---- Claude Code --------------------------------------------------------------

func claudeCredentialsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// ClaudeSignedIn reports whether Claude Code has a login on this machine.
// It checks the file exists and never reads what is in it.
func ClaudeSignedIn() bool {
	fi, err := os.Stat(claudeCredentialsPath())
	return err == nil && !fi.IsDir()
}

// ClaudeNotice explains, for the CLI and API, that a signed-in Claude Code is
// not offered as a provider. ok is false when Claude Code is not signed in.
func ClaudeNotice() (string, bool) {
	if !ClaudeSignedIn() {
		return "", false
	}
	return "Claude Code 已登录，但不作为 Provider 提供：" + claudeReason, true
}
