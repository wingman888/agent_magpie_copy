package provider

// Account providers ride on a login the user already has in another agent:
// Codex (ChatGPT account) and GitHub Copilot. dial never stores their
// credentials. Every use reads the agent's own login file, and the only thing
// dial keeps in providers.json is which of their models the user picked.
//
// Claude Code is the exception that is never offered: dial only checks that
// its login file exists, and reports that in Notices.

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

// Reserved provider ids for account-backed vendors.
const (
	CodexID   = "codex"
	CopilotID = "copilot"
	ClaudeID  = "claude"
)

// CodexBase is the ChatGPT Codex backend; codex/<model> requests go to
// CodexBase + "/responses". Tests point it at a local fake.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// DefaultCopilotAPI is the Copilot API host used until a session names one.
const DefaultCopilotAPI = "https://api.githubcopilot.com"

// Token endpoints. Variables so tests can point them at a fake server.
var (
	codexTokenURL   = "https://auth.openai.com/oauth/token"
	copilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	codexClientID      = "app_EMoamEEZ73f0CkXaXp7hrann" // the codex CLI's client id
	codexRefreshWindow = 5 * time.Minute
)

// ErrCodexSignedOut is what the user sees when a Codex login cannot be refreshed.
var ErrCodexSignedOut = errors.New("Codex 登录已失效，已登出，请运行 `codex login` 重新登录")

// ErrCopilotSignedOut is what the user sees when a Copilot session is rejected.
var ErrCopilotSignedOut = errors.New("GitHub Copilot 会话已失效，请重新登录 Copilot")

// SignedOut is the sign-in advice for an account provider whose login was rejected.
func SignedOut(id string) error {
	if id == CopilotID {
		return ErrCopilotSignedOut
	}
	return ErrCodexSignedOut
}

// IsSignedOut reports whether err says an account login was rejected.
func IsSignedOut(err error) bool {
	return errors.Is(err, ErrCodexSignedOut) || errors.Is(err, ErrCopilotSignedOut)
}

// rejected reports whether an agent's auth endpoint refused the login. Only
// these statuses mean the user has to sign in again; others are real errors.
func rejected(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// claudeReason says why Claude Code is shown but never offered.
const claudeReason = "Claude Code 的订阅令牌只供 Claude Code 自己使用，Anthropic 不允许把它交给其他工具，所以 dial 不把它作为 Provider 提供。"

// IsAccount reports whether an id names an account-backed provider.
func IsAccount(id string) bool { return id == CodexID || id == CopilotID }

// Account says whose login a provider rides on.
type Account struct {
	Provider string `json:"provider"`       // codex or copilot
	Agent    string `json:"agent"`          // the agent that holds the login
	User     string `json:"user"`           // ChatGPT email, or GitHub login
	Plan     string `json:"plan,omitempty"` // subscription plan, when the login says
}

// Notice explains a signed-in agent that dial deliberately does not offer.
type Notice struct {
	Agent  string `json:"agent"`
	Reason string `json:"reason"`
}

// Accounts lists the account providers whose agent is signed in right now.
func Accounts() []Account {
	var out []Account
	if a, ok := codexAccount(); ok {
		out = append(out, a)
	}
	if a, ok := copilotAccount(); ok {
		out = append(out, a)
	}
	return out
}

// AccountOf returns the login behind an account provider.
func AccountOf(p Provider) (Account, bool) {
	for _, a := range Accounts() {
		if a.Provider == p.ID {
			return a, true
		}
	}
	return Account{}, false
}

// Notices lists the signed-in agents dial does not offer, with the reason.
func Notices() []Notice {
	if fileExists(claudeCredentialsPath()) {
		return []Notice{{Agent: "Claude Code", Reason: claudeReason}}
	}
	return nil
}

// accountProviders builds the account providers that are signed in. picks
// holds the models the user chose for each; nothing else is stored.
func accountProviders(picks map[string][]string) []Provider {
	var out []Provider
	for _, a := range Accounts() {
		p := Provider{ID: a.Provider, Models: picks[a.Provider]}
		switch a.Provider {
		case CodexID:
			p.Name, p.Icon, p.Responses = "Codex", "codex-color", CodexBase
		case CopilotID:
			p.Name, p.Icon, p.Chat = "GitHub Copilot", "githubcopilot", DefaultCopilotAPI
		}
		out = append(out, p)
	}
	return out
}

// accountAvailable is the model list of an account provider: the last live
// Copilot list when one was saved, else the built-in list.
func accountAvailable(p Provider) []catalog.Model {
	if live, _, ok := catalog.Live(liveKey(p)); ok {
		return live
	}
	if p.ID == CopilotID {
		return copilotBuiltin()
	}
	return codexBuiltin()
}

// liveKey names the saved model list of a provider. A Copilot list belongs
// to the GitHub login it was fetched for, so another login never reuses it.
func liveKey(p Provider) string {
	if p.ID == CopilotID {
		if u, ok := copilotUser(); ok {
			return CopilotID + "-" + Slug(u)
		}
	}
	return p.ID
}

// fetchAccount refreshes an account provider's model list. Codex has no list
// endpoint dial can use, so it keeps the built-in one. Copilot asks the
// session which models this account may use and remembers the answer.
func fetchAccount(ctx context.Context, p Provider) ([]catalog.Model, error) {
	if p.ID == CodexID {
		return codexBuiltin(), nil
	}
	tok, api, err := CopilotSession(ctx)
	if err != nil {
		return nil, err
	}
	ms, err := copilotModelList(ctx, tok, api)
	if err != nil {
		return nil, err
	}
	return ms, catalog.SaveLive(liveKey(p), api, ms)
}

func codexBuiltin() []catalog.Model {
	return []catalog.Model{
		{ID: "gpt-5.5", Name: "GPT-5.5"},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex"},
	}
}

func copilotBuiltin() []catalog.Model {
	return []catalog.Model{
		{ID: "gpt-5-mini", Name: "GPT-5 mini"},
		{ID: "gpt-4.1", Name: "GPT-4.1"},
		{ID: "claude-sonnet-4.5", Name: "Claude Sonnet 4.5"},
		{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
	}
}

// ---- paths and small helpers ------------------------------------------------

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func codexAuthPath() string { return filepath.Join(homeDir(), ".codex", "auth.json") }

func copilotAppsPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot", "apps.json")
	}
	return filepath.Join(homeDir(), ".config", "github-copilot", "apps.json")
}

func claudeCredentialsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(homeDir(), ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeAtomic replaces a file in one step: the bytes go to a temp file beside
// it, which is then renamed over the old one, so a crash never leaves a
// half-written login file behind.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// jwtClaims reads the payload of a JWT without checking its signature; dial
// only needs the email, plan and expiry the agents already trust.
func jwtClaims(tok string) (map[string]any, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func tokenExpiry(tok string) (time.Time, bool) {
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

// ---- Codex -------------------------------------------------------------------

// codexLogin reads the Codex login file. It is a login only in ChatGPT mode;
// an API-key login is not an account.
func codexLogin() (map[string]any, map[string]string, bool) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc map[string]any
	if dec.Decode(&doc) != nil {
		return nil, nil, false
	}
	if mode, _ := doc["auth_mode"].(string); mode != "chatgpt" {
		return nil, nil, false
	}
	t, _ := doc["tokens"].(map[string]any)
	tk := map[string]string{}
	for _, k := range []string{"id_token", "access_token", "refresh_token", "account_id"} {
		s, _ := t[k].(string)
		tk[k] = s
	}
	if tk["access_token"] == "" && tk["refresh_token"] == "" {
		return nil, nil, false
	}
	return doc, tk, true
}

func codexAccount() (Account, bool) {
	_, tk, ok := codexLogin()
	if !ok {
		return Account{}, false
	}
	claims, _ := jwtClaims(tk["id_token"])
	email, _ := claims["email"].(string)
	plan := ""
	if info, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		plan, _ = info["chatgpt_plan_type"].(string)
	}
	user := email
	if user == "" {
		user = tk["account_id"]
	}
	return Account{Provider: CodexID, Agent: "codex", User: user, Plan: plan}, true
}

// CodexCredentials returns the access token and account id of the signed-in
// Codex account. The login file is read on every call. A token that expires
// within five minutes is refreshed the way the codex CLI does it, and written
// back to the same file with every other field left as it was.
func CodexCredentials(ctx context.Context) (token, accountID string, err error) {
	doc, tk, ok := codexLogin()
	if !ok {
		return "", "", errors.New("Codex 未以 ChatGPT 账号登录，请运行 `codex login`")
	}
	exp, known := tokenExpiry(tk["access_token"])
	if tk["access_token"] == "" || (known && time.Until(exp) < codexRefreshWindow) {
		if tk, err = refreshCodex(ctx, doc, tk); err != nil {
			return "", "", err
		}
	}
	acct := tk["account_id"]
	if acct == "" {
		claims, _ := jwtClaims(tk["id_token"])
		if info, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			acct, _ = info["chatgpt_account_id"].(string)
		}
	}
	return tk["access_token"], acct, nil
}

// refreshCodex trades the refresh token for new tokens and saves them into
// the Codex login file.
func refreshCodex(ctx context.Context, doc map[string]any, tk map[string]string) (map[string]string, error) {
	if tk["refresh_token"] == "" {
		return nil, ErrCodexSignedOut
	}
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": tk["refresh_token"],
		"scope":         "openid profile email",
	})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Codex 令牌刷新失败：%w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		if rejected(res.StatusCode) {
			return nil, ErrCodexSignedOut
		}
		return nil, fmt.Errorf("Codex 令牌刷新失败：%s", APIError(b, res.Status))
	}
	var r struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &r) != nil || r.AccessToken == "" {
		return nil, errors.New("Codex 令牌刷新返回的内容不对")
	}

	t, _ := doc["tokens"].(map[string]any)
	if t == nil {
		t = map[string]any{}
	}
	t["access_token"] = r.AccessToken
	if r.IDToken != "" {
		t["id_token"] = r.IDToken
	}
	if r.RefreshToken != "" {
		t["refresh_token"] = r.RefreshToken
	}
	doc["tokens"] = t
	doc["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(codexAuthPath(), append(out, '\n'), 0o600); err != nil {
		return nil, err
	}

	next := make(map[string]string, len(tk))
	for k, v := range tk {
		next[k] = v
	}
	next["access_token"] = r.AccessToken
	if r.IDToken != "" {
		next["id_token"] = r.IDToken
	}
	if r.RefreshToken != "" {
		next["refresh_token"] = r.RefreshToken
	}
	return next, nil
}

// ---- Copilot -----------------------------------------------------------------

type copilotApp struct {
	User  string `json:"user"`
	OAuth string `json:"oauth_token"`
}

// copilotLogin reads the GitHub token the editor or Copilot CLI stored. Each
// entry is read on its own, so one malformed entry does not hide the others.
func copilotLogin() (user, oauth string, ok bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return "", "", false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return "", "", false
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, "github.com") {
			continue
		}
		var app copilotApp
		if json.Unmarshal(raw[k], &app) != nil || app.OAuth == "" {
			continue
		}
		return app.User, app.OAuth, true
	}
	return "", "", false
}

// copilotUser is the GitHub login of the signed-in Copilot account.
func copilotUser() (string, bool) {
	user, _, ok := copilotLogin()
	if !ok {
		return "", false
	}
	if user == "" {
		user = "github"
	}
	return user, true
}

func copilotAccount() (Account, bool) {
	user, ok := copilotUser()
	if !ok {
		return Account{}, false
	}
	return Account{Provider: CopilotID, Agent: "copilot", User: user}, true
}

// CopilotHeaders are the headers the Copilot API expects from an editor,
// for the given session token.
func CopilotHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization":          "Bearer " + token,
		"Editor-Version":         "vscode/1.99.0",
		"Editor-Plugin-Version":  "copilot-chat/0.26.0",
		"Copilot-Integration-Id": "vscode-chat",
	}
}

// setCopilotHeaders sends the headers the Copilot API expects from an editor.
func setCopilotHeaders(req *http.Request, token string) {
	for k, v := range CopilotHeaders(token) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
}

// copilotStatus is the error for a Copilot reply that was not 200. Only a
// rejected login asks the user to sign in again.
func copilotStatus(res *http.Response, b []byte, what string) error {
	if rejected(res.StatusCode) {
		return ErrCopilotSignedOut
	}
	return fmt.Errorf("%s：%s", what, APIError(b, res.Status))
}

// CopilotSession trades the GitHub token for a short-lived Copilot session
// and returns it with the API endpoint that session names. Nothing is cached.
func CopilotSession(ctx context.Context) (token, api string, err error) {
	_, oauth, ok := copilotLogin()
	if !ok {
		return "", "", errors.New("GitHub Copilot 未登录，请先在编辑器或 Copilot CLI 中登录 GitHub")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "dial")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("GitHub Copilot 会话获取失败：%w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", "", copilotStatus(res, b, "GitHub Copilot 会话获取失败")
	}
	var r struct {
		Token     string `json:"token"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &r) != nil || r.Token == "" {
		return "", "", errors.New("GitHub Copilot 会话返回的内容不对")
	}
	api = strings.TrimRight(r.Endpoints.API, "/")
	if api == "" {
		api = DefaultCopilotAPI
	}
	return r.Token, api, nil
}

type copilotRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Capabilities struct {
		Type string `json:"type"`
	} `json:"capabilities"`
	PickerEnabled *bool `json:"model_picker_enabled"`
	Policy        *struct {
		State string `json:"state"`
	} `json:"policy"`
}

// copilotModelList asks the session for its models and keeps the ones this
// account may use in chat: no embeddings, no "auto", no internal models, and
// nothing the account has not enabled.
func copilotModelList(ctx context.Context, token, api string) ([]catalog.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/models", nil)
	if err != nil {
		return nil, err
	}
	setCopilotHeaders(req, token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub Copilot 模型列表获取失败：%w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, copilotStatus(res, b, "GitHub Copilot 模型列表获取失败")
	}
	var v struct {
		Data []copilotRow `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("GitHub Copilot 模型列表格式不对：%w", err)
	}
	var out []catalog.Model
	for _, r := range v.Data {
		if !copilotChatModel(r) {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.ID
		}
		out = append(out, catalog.Model{ID: r.ID, Name: name})
	}
	if len(out) == 0 {
		return nil, errors.New("这个 GitHub Copilot 账号没有可用的聊天模型")
	}
	return out, nil
}

func copilotChatModel(r copilotRow) bool {
	switch {
	case r.ID == "" || r.ID == "auto":
		return false
	case strings.Contains(r.ID, "embed") || r.Capabilities.Type != "chat":
		return false
	case r.PickerEnabled != nil && !*r.PickerEnabled: // internal use
		return false
	case r.Policy != nil && r.Policy.State != "" && r.Policy.State != "enabled":
		return false
	}
	return true
}
