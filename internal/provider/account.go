package provider

// An agent the user is already signed in to can lend its subscription to
// other agents. The agent's own credential file is the only source: dial reads
// it each time it needs it, never copies a token into its own config, and
// writes back only what a token refresh changed (in Codex's case).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Ids of the account providers.
const (
	codexID   = "codex"
	copilotID = "copilot"
)

// Endpoints. Variables, so tests can point them at stand-ins.
var (
	// CodexBase is the ChatGPT backend Codex talks to.
	CodexBase = "https://chatgpt.com/backend-api/codex"
	// CodexRefreshURL is the OpenAI token endpoint used to refresh a login.
	CodexRefreshURL = "https://auth.openai.com/oauth/token"
	// CopilotTokenURL exchanges a GitHub OAuth token for a Copilot session token.
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	codexClientID      = "app_EMoamEEZ73f0CkXaXp7hrann"
	refreshWithin      = 5 * time.Minute
	copilotIntegration = "vscode-chat"
	copilotEditor      = "vscode/1.99.3"
	copilotPlugin      = "copilot-chat/0.26.7"
	copilotUA          = "GitHubCopilotChat/0.26.7"
	claudeWhy          = "Anthropic 条款只允许 Claude 订阅用于 Claude Code 本身，dial 因此不把它作为 Provider 提供。如需经 dial 使用 Claude 模型，请改用 Anthropic API Key 作为 Provider。"
)

// codexDropped are the sampling and output-limit parameters the ChatGPT
// backend refuses.
var codexDropped = []string{
	"max_output_tokens", "max_completion_tokens", "max_tokens",
	"temperature", "top_p", "presence_penalty", "frequency_penalty",
}

// Account says whose subscription a provider borrows. It never holds a token.
type Account struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
}

// Exclusion is a sign-in dial recognises but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// ErrSignedOut matches every "log in again" failure of an account provider.
var ErrSignedOut = errors.New("已登出，请重新登录")

type signedOutError struct {
	agent, hint string
	cause       error
}

func (e *signedOutError) Error() string {
	s := fmt.Sprintf("%s 已登出，请重新登录（%s）", e.agent, e.hint)
	if e.cause != nil {
		s += "：" + e.cause.Error()
	}
	return s
}

func (e *signedOutError) Unwrap() error { return e.cause }

func (e *signedOutError) Is(target error) bool { return target == ErrSignedOut }

func signedOut(agent, hint string, cause error) error {
	return &signedOutError{agent: agent, hint: hint, cause: cause}
}

func isAccountID(id string) bool { return id == codexID || id == copilotID }

func accountName(id string) string {
	if id == codexID {
		return "Codex"
	}
	return "Copilot"
}

// storedUserBuilt reports whether the config holds a user-built provider (one
// with an address) under this id; that one wins over the account.
func storedUserBuilt(stored []Provider, id string) bool {
	for _, p := range stored {
		if p.ID == id && userBuilt(p) {
			return true
		}
	}
	return false
}

// saveAccount keeps only the model picks of an account provider.
func saveAccount(id string, models []string) error {
	f := load()
	rec := Provider{ID: id, Models: cleanList(models)}
	for i := range f.Providers {
		if f.Providers[i].ID == id {
			f.Providers[i] = rec
			return store(f)
		}
	}
	f.Providers = append(f.Providers, rec)
	return store(f)
}

// Accounts lists the account providers whose agent is signed in right now.
// Each carries the model picks saved for it, and no credential.
func Accounts() []Provider {
	stored := load().Providers
	var out []Provider
	for _, id := range []string{codexID, copilotID} {
		if storedUserBuilt(stored, id) {
			continue
		}
		acc, ok := signedInAccount(id)
		if !ok {
			continue
		}
		p := Provider{ID: id, Name: accountName(id), Account: acc}
		for _, s := range stored {
			if s.ID == id {
				p.Models = cleanList(s.Models)
			}
		}
		out = append(out, p)
	}
	return out
}

func signedIn(id string) bool {
	_, ok := signedInAccount(id)
	return ok
}

func signedInAccount(id string) (*Account, bool) {
	switch id {
	case codexID:
		a, ok := readCodex()
		if !ok {
			return nil, false
		}
		email, plan := codexIdentity(a)
		return &Account{Agent: codexID, User: email, Plan: plan}, true
	case copilotID:
		l, ok := readCopilot()
		if !ok {
			return nil, false
		}
		return &Account{Agent: copilotID, User: l.User}, true
	}
	return nil, false
}

// Excluded lists sign-ins dial recognises but does not offer. Only the
// keychain probe is cached (for a minute): it runs a subprocess.
func Excluded() []Exclusion {
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	return out
}

// claudeSignedIn only checks that a credential exists. Its content is never
// read, and on macOS the keychain is asked for existence alone (no -w, so no
// secret is printed and no access prompt is expected).
func claudeSignedIn() bool {
	var dirs []string
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".claude"))
	}
	for _, d := range dirs {
		for _, name := range []string{".credentials.json", "credentials.json"} {
			if fi, err := os.Stat(filepath.Join(d, name)); err == nil && !fi.IsDir() {
				return true
			}
		}
	}
	return claudeKeychainHas()
}

var claudeKC struct {
	sync.Mutex
	at  time.Time
	has bool
}

func claudeKeychainHas() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	claudeKC.Lock()
	defer claudeKC.Unlock()
	if !claudeKC.at.IsZero() && time.Since(claudeKC.at) < time.Minute {
		return claudeKC.has
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials")
	cmd.Stdout = nil
	cmd.Stderr = nil
	claudeKC.has = cmd.Run() == nil
	claudeKC.at = time.Now()
	return claudeKC.has
}

// ---- Codex ------------------------------------------------------------------

type codexAuth struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

type codexTokens struct {
	AccessToken, IDToken, RefreshToken string
}

func codexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodex reads Codex's login as it is on disk now. ok is false unless
// Codex is signed in with ChatGPT (not an API key).
func readCodex() (codexAuth, bool) {
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return codexAuth{}, false
	}
	var a codexAuth
	if json.Unmarshal(b, &a) != nil {
		return codexAuth{}, false
	}
	if strings.EqualFold(strings.TrimSpace(a.AuthMode), "apikey") || a.Tokens.AccessToken == "" {
		return a, false
	}
	return a, true
}

// codexIdentity reads the display name and plan from the id token. The
// payload is decoded only; the signature is not checked.
func codexIdentity(a codexAuth) (email, plan string) {
	claims := jwtClaims(a.Tokens.IDToken)
	email, _ = claims["email"].(string)
	if email == "" {
		email = "ChatGPT"
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		plan, _ = auth["chatgpt_plan_type"].(string)
	}
	return email, plan
}

func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
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

// codexNearExpiry is true when the access token expires within refreshWithin.
// A token without a readable exp is treated as fresh.
func codexNearExpiry(access string) bool {
	claims := jwtClaims(access)
	exp, ok := claims["exp"].(float64)
	if !ok {
		return false
	}
	return time.Until(time.Unix(int64(exp), 0)) < refreshWithin
}

// codexMu serialises refreshes, so concurrent requests refresh only once.
var codexMu sync.Mutex

// refreshCodex refreshes the login and writes the new tokens back to Codex's
// own auth.json. It re-reads the file under the lock, so a refresh made by a
// request that got there first is reused.
func refreshCodex(ctx context.Context) (codexAuth, error) {
	const hint = "运行 codex login"
	codexMu.Lock()
	defer codexMu.Unlock()
	a, ok := readCodex()
	if !ok {
		return codexAuth{}, signedOut(codexID, hint, nil)
	}
	if !codexNearExpiry(a.Tokens.AccessToken) {
		return a, nil
	}
	if a.Tokens.RefreshToken == "" {
		return codexAuth{}, signedOut(codexID, hint, errors.New("没有 refresh_token"))
	}
	fresh, err := postCodexRefresh(ctx, a.Tokens.RefreshToken)
	if err != nil {
		return codexAuth{}, signedOut(codexID, hint, err)
	}
	if err := writeCodexTokens(fresh); err != nil {
		return codexAuth{}, fmt.Errorf("写回 codex 凭据失败: %w", err)
	}
	a.Tokens.AccessToken = fresh.AccessToken
	if fresh.IDToken != "" {
		a.Tokens.IDToken = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		a.Tokens.RefreshToken = fresh.RefreshToken
	}
	return a, nil
}

func postCodexRefresh(ctx context.Context, refreshToken string) (codexTokens, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexTokens{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexTokens{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexTokens{}, fmt.Errorf("刷新被拒绝（%s）", res.Status)
	}
	var v struct {
		AccessToken  string `json:"access_token"`
		IDToken      string `json:"id_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &v) != nil || v.AccessToken == "" {
		return codexTokens{}, errors.New("刷新响应无效")
	}
	return codexTokens{AccessToken: v.AccessToken, IDToken: v.IDToken, RefreshToken: v.RefreshToken}, nil
}

// writeCodexTokens updates only the token fields in auth.json; every other
// field is kept as it was. The file stays private (0600) and is replaced
// atomically.
func writeCodexTokens(t codexTokens) error {
	p := codexPath()
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	top := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &top); err != nil {
		return err
	}
	if top == nil {
		top = map[string]json.RawMessage{}
	}
	tokens := map[string]json.RawMessage{}
	if t0, ok := top["tokens"]; ok {
		if err := json.Unmarshal(t0, &tokens); err != nil {
			return err
		}
	}
	if tokens == nil {
		tokens = map[string]json.RawMessage{}
	}
	set := func(k, v string) {
		if v == "" {
			return
		}
		b, _ := json.Marshal(v)
		tokens[k] = b
	}
	set("access_token", t.AccessToken)
	set("id_token", t.IDToken)
	set("refresh_token", t.RefreshToken)
	tb, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	top["tokens"] = tb
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(p, append(out, '\n'), 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func signCodex(ctx context.Context, req *http.Request) error {
	a, ok := readCodex()
	if !ok {
		return signedOut(codexID, "运行 codex login", nil)
	}
	if codexNearExpiry(a.Tokens.AccessToken) {
		var err error
		if a, err = refreshCodex(ctx); err != nil {
			return err
		}
	}
	u, err := url.Parse(CodexBase + "/responses")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+a.Tokens.AccessToken)
	if a.Tokens.AccountID != "" {
		req.Header.Set("chatgpt-account-id", a.Tokens.AccountID)
	}
	return nil
}

// ---- Copilot ----------------------------------------------------------------

type copilotLogin struct {
	User, OAuth string
}

func copilotConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "github-copilot")
}

// readCopilot finds the GitHub login in apps.json or hosts.json.
func readCopilot() (copilotLogin, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotConfigDir(), name))
		if err != nil {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(b, &entries) != nil {
			continue
		}
		var keys []string
		for k := range entries {
			if strings.HasPrefix(k, "github.com") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			var e struct {
				User  string `json:"user"`
				OAuth string `json:"oauth_token"`
			}
			if json.Unmarshal(entries[k], &e) != nil || e.OAuth == "" {
				continue
			}
			if e.User == "" {
				e.User = "GitHub"
			}
			return copilotLogin{User: e.User, OAuth: e.OAuth}, true
		}
	}
	return copilotLogin{}, false
}

const copilotHint = "在编辑器或 Copilot CLI 中重新登录 GitHub"

type copilotSess struct {
	oauth, token, api string
	exp               time.Time
}

var (
	copilotMu  sync.Mutex
	copilotCur copilotSess
)

// copilotSession returns a Copilot session token for the login, reusing the
// cached one until it is close to expiry.
func copilotSession(ctx context.Context, oauth string) (copilotSess, error) {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if copilotCur.oauth == oauth && copilotCur.token != "" && time.Until(copilotCur.exp) > refreshWithin {
		return copilotCur, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSess{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUA)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSess{}, fmt.Errorf("连接 GitHub Copilot 失败: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return copilotSess{}, signedOut(copilotID, copilotHint, fmt.Errorf("GitHub 返回 %s", res.Status))
	case http.StatusOK:
	default:
		return copilotSess{}, errors.New(APIError(b, res.Status))
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" || v.Endpoints.API == "" {
		return copilotSess{}, errors.New("Copilot 令牌响应无效")
	}
	exp := time.Now().Add(20 * time.Minute)
	if v.ExpiresAt > 0 {
		exp = time.Unix(v.ExpiresAt, 0)
	}
	copilotCur = copilotSess{oauth: oauth, token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/"), exp: exp}
	return copilotCur, nil
}

func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	login, ok := readCopilot()
	if !ok {
		return signedOut(copilotID, copilotHint, nil)
	}
	s, err := copilotSession(ctx, login.OAuth)
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
	req.Header.Set("Copilot-Integration-Id", copilotIntegration)
	req.Header.Set("Editor-Version", copilotEditor)
	req.Header.Set("Editor-Plugin-Version", copilotPlugin)
	req.Header.Set("User-Agent", copilotUA)
	req.Header.Set("X-Initiator", initiator(body))
	if hasImage(body) {
		req.Header.Set("Copilot-Vision-Request", "true")
	} else {
		req.Header.Del("Copilot-Vision-Request")
	}
	return nil
}

// initiator says who started the turn: a user message last means the user
// did; anything else (a tool result, an assistant turn) means the agent did.
func initiator(body []byte) string {
	var v struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return "agent"
	}
	if v.Messages[len(v.Messages)-1].Role == "user" {
		return "user"
	}
	return "agent"
}

func hasImage(body []byte) bool {
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
			if p.Type == "image_url" || p.Type == "image" {
				return true
			}
		}
	}
	return false
}

// internalModel is true for Copilot's helper and internal model ids.
func internalModel(id string) bool {
	for _, pre := range []string{"copilot-search", "exec-agent", "trajectory"} {
		if strings.HasPrefix(id, pre) {
			return true
		}
	}
	for _, suf := range []string{"-secondary", "-tertiary", "-4th", "-free-auto"} {
		if strings.HasSuffix(id, suf) {
			return true
		}
	}
	return false
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

// parseCopilotModels keeps the chat models the account may pick, in the
// order Copilot returned them.
func parseCopilotModels(b []byte) ([]catalog.Model, error) {
	var v struct {
		Data []copilotModel `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("Copilot 模型清单无效: %w", err)
	}
	var out []catalog.Model
	for _, m := range v.Data {
		if m.Capabilities.Type != "chat" || internalModel(m.ID) {
			continue
		}
		if !m.ModelPickerEnabled && m.Policy.State != "enabled" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name, Efforts: m.Capabilities.Supports.ReasoningEffort})
	}
	return out, nil
}

// refreshCopilot asks Copilot which chat models this account may use and
// remembers the answer (the endpoint, never a token).
func refreshCopilot(ctx context.Context) ([]catalog.Model, error) {
	login, ok := readCopilot()
	if !ok {
		return nil, signedOut(copilotID, copilotHint, nil)
	}
	s, err := copilotSession(ctx, login.OAuth)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Copilot-Integration-Id", copilotIntegration)
	req.Header.Set("Editor-Version", copilotEditor)
	req.Header.Set("Editor-Plugin-Version", copilotPlugin)
	req.Header.Set("User-Agent", copilotUA)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("读取 Copilot 模型清单失败: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	switch res.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, signedOut(copilotID, copilotHint, fmt.Errorf("Copilot 返回 %s", res.Status))
	case http.StatusOK:
	default:
		return nil, errors.New(APIError(b, res.Status))
	}
	ms, err := parseCopilotModels(b)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, errors.New("这个 Copilot 账号没有可用的聊天模型")
	}
	if err := catalog.SaveLive(copilotID, s.api, ms); err != nil {
		return nil, err
	}
	return ms, nil
}

// ---- shared -----------------------------------------------------------------

// accountModels is what an account provider offers before any refresh.
func accountModels(p Provider) []catalog.Model {
	switch p.Account.Agent {
	case codexID:
		return append([]catalog.Model(nil), catalog.Builtin(codexID)...)
	case copilotID:
		if live, _, ok := catalog.Live(copilotID); ok {
			return live
		}
		var out []catalog.Model
		for _, m := range catalog.Builtin(copilotID) {
			if m.ID != "auto" {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	switch p.Account.Agent {
	case codexID:
		return accountModels(p), nil
	case copilotID:
		return refreshCopilot(ctx)
	}
	return nil, fmt.Errorf("%s is not an account provider", p.ID)
}

// Sign adds the credentials a request to this provider needs. Ordinary
// providers get their key; account providers get the agent's token, and the
// request URL is pointed at the backend that agent uses.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case codexID:
		return signCodex(ctx, req)
	case copilotID:
		return signCopilot(ctx, req, body)
	}
	return fmt.Errorf("%s is not an account provider", p.ID)
}

// Prepare adjusts a request body for the backend an account provider uses:
// the model is named without its prefix, and for Codex the body is made
// streamed, unstored, list-shaped, and free of parameters the backend refuses.
// Ordinary providers get the body back unchanged.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil {
		return body
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, p.ID+"/")
	}
	if p.Account.Agent == codexID {
		m["stream"] = true
		m["store"] = false
		if s, ok := m["input"].(string); ok {
			m["input"] = []any{map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "input_text", "text": s}},
			}}
		}
		for _, k := range codexDropped {
			delete(m, k)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
