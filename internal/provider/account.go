package provider

// Accounts: agents the user already signed in to (Codex with a ChatGPT
// account, Copilot with a GitHub account) become providers of their own.
// The agent's credential file is the one source of truth: it is read on
// every use and never copied into dial's config. Sign out in the agent and
// the provider is gone; the agent refreshes its token and dial uses the new
// one. dial only remembers which of the account's models to expose.
//
// Claude Code's sign-in is recognised but never offered: Anthropic's terms
// allow a Claude subscription in Claude Code alone. dial only checks that a
// sign-in exists, never reads it.

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
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account is who an account provider is signed in as. It never holds a token.
type Account struct {
	Agent string `json:"agent"`          // "codex" or "copilot"
	User  string `json:"user"`           // email or GitHub login
	Plan  string `json:"plan,omitempty"` // subscription tier, when known
}

// Exclusion is a sign-in dial recognises but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// Account provider ids.
const (
	CodexID   = "codex"
	CopilotID = "copilot"
)

var (
	// CodexBase is the ChatGPT backend Codex talks to with a ChatGPT sign-in.
	CodexBase = "https://chatgpt.com/backend-api/codex"
	// CodexRefreshURL is where Codex refreshes its ChatGPT tokens.
	CodexRefreshURL = "https://auth.openai.com/oauth/token"
	// CopilotTokenURL exchanges a GitHub token for a Copilot session token.
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	// copilotAPI is where Copilot lives until a session token says otherwise.
	copilotAPI = "https://api.githubcopilot.com"

	copilotUA            = "GitHubCopilotChat/0.26.7"
	copilotEditor        = "vscode/1.99.3"
	copilotPlugin        = "copilot-chat/0.26.7"
	copilotIntegrationID = "vscode-chat"
)

// ClaudeWhy is why a Claude Code sign-in is not offered.
const ClaudeWhy = "Anthropic's terms only allow a Claude subscription to be used in Claude Code itself; " +
	"add an Anthropic API key as a provider instead (dial provider add anthropic sk-ant-…)"

// ErrSignedOut marks a request that failed because the agent's sign-in is
// gone or no longer valid.
var ErrSignedOut = errors.New("signed out")

type signedOutError struct{ msg string }

func (e *signedOutError) Error() string        { return e.msg }
func (e *signedOutError) Is(target error) bool { return target == ErrSignedOut }

func codexSignedOut(detail string) error {
	msg := "codex: signed out or token expired, please run `codex login` again (已登出或令牌过期，请重新登录)"
	if detail != "" {
		msg += ": " + detail
	}
	return &signedOutError{msg}
}

func copilotSignedOut(detail string) error {
	msg := "copilot: signed out or GitHub token no longer valid, please sign in to GitHub Copilot again (已登出或令牌失效，请重新登录)"
	if detail != "" {
		msg += ": " + detail
	}
	return &signedOutError{msg}
}

// accountID reports whether an id is one an account provider uses.
func accountID(id string) bool { return id == CodexID || id == CopilotID }

func hasBase(p Provider) bool { return p.Chat != "" || p.Responses != "" || p.Anthropic != "" }

// AgentName is how an account's agent is shown.
func AgentName(agent string) string {
	switch agent {
	case CodexID:
		return "Codex"
	case CopilotID:
		return "Copilot"
	case "claude":
		return "Claude Code"
	}
	return agent
}

// Accounts lists the signed-in agent accounts dial offers as providers. A
// provider the user built with the same id (one with a base URL) wins.
func Accounts() []Provider {
	var out []Provider
	for _, p := range All() {
		if p.Account != nil {
			out = append(out, p)
		}
	}
	return out
}

// signedIn reads the agents' credential files for accounts, fresh each call.
func signedIn() []Provider {
	var out []Provider
	if a, ok := readCodexAuth(); ok && a.signedIn() {
		out = append(out, codexProvider(a))
	}
	if l, ok := readCopilotLogin(); ok {
		out = append(out, copilotProvider(l))
	}
	return out
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// ---- Codex -------------------------------------------------------------------

type codexAuth struct {
	Mode   string  `json:"auth_mode"`
	APIKey *string `json:"OPENAI_API_KEY"`
	Tokens *struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

func codexAuthPath() string { return filepath.Join(homeDir(), ".codex", "auth.json") }

func readCodexAuth() (codexAuth, bool) {
	var a codexAuth
	b, err := os.ReadFile(codexAuthPath())
	if err != nil || json.Unmarshal(b, &a) != nil {
		return a, false
	}
	return a, true
}

// signedIn is true for a ChatGPT sign-in; API-key mode is not an account.
func (a codexAuth) signedIn() bool {
	switch strings.ToLower(strings.TrimSpace(a.Mode)) {
	case "apikey", "api_key", "api-key":
		return false
	}
	return a.Tokens != nil && a.Tokens.AccessToken != ""
}

func (a codexAuth) accountID() string {
	if a.Tokens == nil {
		return ""
	}
	if a.Tokens.AccountID != "" {
		return a.Tokens.AccountID
	}
	var c struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	jwtClaims(a.Tokens.IDToken, &c)
	return c.Auth.AccountID
}

func codexProvider(a codexAuth) Provider {
	var c struct {
		Email string `json:"email"`
		Auth  struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if a.Tokens != nil {
		jwtClaims(a.Tokens.IDToken, &c)
	}
	user := strings.TrimSpace(c.Email)
	if user == "" {
		user = "ChatGPT"
	}
	return Provider{
		ID: CodexID, Name: "Codex", Icon: "codex-color", Catalog: "openai",
		Responses: strings.TrimRight(CodexBase, "/"),
		Website:   "https://chatgpt.com",
		Account:   &Account{Agent: CodexID, User: user, Plan: strings.TrimSpace(c.Auth.Plan)},
	}
}

// jwtClaims decodes a JWT's payload into v without verifying it.
func jwtClaims(tok string, v any) bool {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return false
	}
	seg := strings.TrimRight(parts[1], "=")
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(seg); err != nil {
			return false
		}
	}
	return json.Unmarshal(b, v) == nil
}

func jwtExpiry(tok string) time.Time {
	var c struct {
		Exp json.Number `json:"exp"`
	}
	if !jwtClaims(tok, &c) || c.Exp == "" {
		return time.Time{}
	}
	f, err := c.Exp.Float64()
	if err != nil || f <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(f), 0)
}

var codexMu sync.Mutex

// codexToken returns a usable access token and the account id, refreshing
// the token the way Codex does when it is about to expire.
func codexToken(ctx context.Context) (string, string, error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	a, ok := readCodexAuth()
	if !ok || !a.signedIn() {
		return "", "", codexSignedOut("no ChatGPT sign-in in " + codexAuthPath())
	}
	if exp := jwtExpiry(a.Tokens.AccessToken); !exp.IsZero() && time.Until(exp) < 5*time.Minute {
		if err := refreshCodex(ctx, a.Tokens.RefreshToken); err != nil {
			return "", "", err
		}
		if a, ok = readCodexAuth(); !ok || !a.signedIn() {
			return "", "", codexSignedOut("")
		}
	}
	return a.Tokens.AccessToken, a.accountID(), nil
}

// refreshCodex trades the refresh token for new tokens and writes them back
// to Codex's auth.json, keeping every other field.
func refreshCodex(ctx context.Context, refresh string) error {
	if refresh == "" {
		return codexSignedOut("no refresh token")
	}
	u := CodexRefreshURL
	if o := os.Getenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE"); o != "" {
		u = o
	}
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": refresh, "scope": "openid profile email"})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return codexSignedOut(err.Error())
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexSignedOut("token refresh failed: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return codexSignedOut("token refresh failed: " + APIError(b, res.Status))
	}
	var t struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &t) != nil || t.AccessToken == "" {
		return codexSignedOut("token refresh returned no access token")
	}
	if err := writeCodexTokens(t.IDToken, t.AccessToken, t.RefreshToken); err != nil {
		return fmt.Errorf("codex: could not save refreshed token: %w", err)
	}
	return nil
}

func writeCodexTokens(idToken, access, refresh string) error {
	path := codexAuthPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return err
	}
	tokens := map[string]json.RawMessage{}
	if raw, ok := top["tokens"]; ok {
		json.Unmarshal(raw, &tokens)
		if tokens == nil {
			tokens = map[string]json.RawMessage{}
		}
	}
	set := func(k, v string) {
		if v != "" {
			j, _ := json.Marshal(v)
			tokens[k] = j
		}
	}
	set("id_token", idToken)
	set("access_token", access)
	set("refresh_token", refresh)
	tj, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	top["tokens"] = tj
	now, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	top["last_refresh"] = now
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(out, '\n'))
}

// writePrivate replaces a file atomically, readable by the owner only.
func writePrivate(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func signCodex(ctx context.Context, req *http.Request) error {
	tok, acct, err := codexToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if acct != "" {
		req.Header.Set("chatgpt-account-id", acct)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("Accept", "text/event-stream")
	return nil
}

// codexDropped are request fields the ChatGPT backend rejects.
var codexDropped = []string{"max_output_tokens", "max_tokens", "max_completion_tokens", "temperature", "top_p", "top_k",
	"frequency_penalty", "presence_penalty", "seed", "logprobs", "top_logprobs", "n", "stop"}

// prepareCodex shapes a Responses request the way the ChatGPT backend
// wants it: streamed, not stored, input as a list of items.
func prepareCodex(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	m["stream"] = true
	m["store"] = false
	switch in := m["input"].(type) {
	case string:
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": in}}}}
	case nil:
		m["input"] = []any{}
	case []any:
	default:
		m["input"] = []any{in}
	}
	for _, k := range codexDropped {
		delete(m, k)
	}
	if _, ok := m["instructions"]; !ok {
		m["instructions"] = "You are a helpful assistant."
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- Copilot -----------------------------------------------------------------

type copilotLogin struct {
	User  string
	Token string
}

func copilotDir() string {
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		cfg = filepath.Join(homeDir(), ".config")
	}
	return filepath.Join(cfg, "github-copilot")
}

func readCopilotLogin() (copilotLogin, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var m map[string]struct {
			User       string `json:"user"`
			OAuthToken string `json:"oauth_token"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if e := m[k]; strings.HasPrefix(k, "github.com") && e.OAuthToken != "" {
				return copilotLogin{User: e.User, Token: e.OAuthToken}, true
			}
		}
	}
	return copilotLogin{}, false
}

func copilotProvider(l copilotLogin) Provider {
	user := strings.TrimSpace(l.User)
	if user == "" {
		user = "GitHub"
	}
	return Provider{
		ID: CopilotID, Name: "Copilot", Icon: "githubcopilot",
		Chat:    copilotAPI,
		Website: "https://github.com/features/copilot",
		Account: &Account{Agent: CopilotID, User: user},
	}
}

type copilotSession struct {
	token   string
	api     string
	expires time.Time
}

var (
	copilotMu       sync.Mutex
	copilotSessions = map[string]copilotSession{}
)

func copilotHeaders(h http.Header) {
	h.Set("User-Agent", copilotUA)
	h.Set("Editor-Version", copilotEditor)
	h.Set("Editor-Plugin-Version", copilotPlugin)
	h.Set("Copilot-Integration-Id", copilotIntegrationID)
}

// copilotSessionFor trades the GitHub token for a short-lived Copilot token,
// reusing the last one until it is about to expire.
func copilotSessionFor(ctx context.Context) (copilotSession, error) {
	l, ok := readCopilotLogin()
	if !ok {
		return copilotSession{}, copilotSignedOut("no GitHub sign-in in " + copilotDir())
	}
	copilotMu.Lock()
	defer copilotMu.Unlock()
	key := CopilotTokenURL + "\x00" + l.Token
	if s, ok := copilotSessions[key]; ok && time.Until(s.expires) > 60*time.Second {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+l.Token)
	req.Header.Set("Accept", "application/json")
	copilotHeaders(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("copilot: token exchange failed: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound {
		delete(copilotSessions, key)
		return copilotSession{}, copilotSignedOut(APIError(b, res.Status))
	}
	if res.StatusCode != http.StatusOK {
		return copilotSession{}, fmt.Errorf("copilot: token exchange failed: %s", APIError(b, res.Status))
	}
	var v struct {
		Token     string      `json:"token"`
		ExpiresAt json.Number `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSession{}, copilotSignedOut("token exchange returned no token")
	}
	s := copilotSession{token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/")}
	if s.api == "" {
		s.api = copilotAPI
	}
	if f, err := v.ExpiresAt.Float64(); err == nil && f > 0 {
		s.expires = time.Unix(int64(f), 0)
	} else {
		s.expires = time.Now().Add(20 * time.Minute)
	}
	copilotSessions[key] = s
	return s, nil
}

func signCopilot(ctx context.Context, p Provider, req *http.Request, body []byte) error {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return err
	}
	// move the request onto the endpoint the session token names
	u := req.URL.String()
	suffix := "/chat/completions"
	if base := p.Chat; base != "" && strings.HasPrefix(u, base) {
		suffix = strings.TrimPrefix(u, base)
	} else if i := strings.LastIndex(req.URL.Path, "/chat/completions"); i >= 0 {
		suffix = req.URL.Path[i:]
	}
	nu, err := req.URL.Parse(s.api + suffix)
	if err != nil {
		return err
	}
	req.URL = nu
	req.Host = nu.Host
	req.Header.Set("Authorization", "Bearer "+s.token)
	copilotHeaders(req.Header)
	req.Header.Set("Openai-Intent", "conversation-edits")
	initiator, vision := copilotTraits(body)
	req.Header.Set("X-Initiator", initiator)
	if vision {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	return nil
}

// copilotTraits reads who started the turn (the user, or the agent replying
// to a tool) and whether any message carries an image.
func copilotTraits(body []byte) (initiator string, vision bool) {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &v)
	initiator = "agent"
	if n := len(v.Messages); n > 0 && v.Messages[n-1].Role == "user" {
		initiator = "user"
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) == nil {
			for _, p := range parts {
				if p.Type == "image_url" || p.Type == "input_image" || p.Type == "image" {
					vision = true
				}
			}
		}
	}
	return initiator, vision
}

type liveList struct {
	models []catalog.Model
	at     time.Time
}

var (
	accountLiveMu sync.Mutex
	accountLive   = map[string]liveList{}
)

func copilotInternal(id string) bool {
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

// fetchCopilot asks Copilot which chat models this account may use.
func fetchCopilot(ctx context.Context) ([]catalog.Model, error) {
	s, err := copilotSessionFor(ctx)
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
	copilotHeaders(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusUnauthorized {
		return nil, copilotSignedOut(APIError(b, res.Status))
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot: model list: %s", APIError(b, res.Status))
	}
	var v struct {
		Data []struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			PickerOn     bool   `json:"model_picker_enabled"`
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
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("copilot: model list is not JSON")
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range v.Data {
		if m.ID == "" || seen[m.ID] || m.Capabilities.Type != "chat" || copilotInternal(m.ID) {
			continue
		}
		if !m.PickerOn && (m.Policy == nil || m.Policy.State != "enabled") {
			continue
		}
		seen[m.ID] = true
		name := m.Name
		if name == "" {
			name = m.ID
		}
		mm := catalog.Model{ID: m.ID, Name: name}
		if len(m.Capabilities.Supports.Efforts) > 0 {
			mm.Efforts = append([]string(nil), m.Capabilities.Supports.Efforts...)
		}
		out = append(out, mm)
	}
	if len(out) == 0 {
		return nil, errors.New("copilot: this account has no chat models enabled")
	}
	return out, nil
}

// ---- the account side of Provider ----------------------------------------------

// accountAvailable lists an account provider's models.
func (p Provider) accountAvailable() []catalog.Model {
	accountLiveMu.Lock()
	l, ok := accountLive[p.ID]
	accountLiveMu.Unlock()
	if ok && len(l.models) > 0 {
		return append([]catalog.Model(nil), l.models...)
	}
	switch p.Account.Agent {
	case CodexID:
		return append([]catalog.Model(nil), catalog.Builtin("codex")...)
	case CopilotID:
		var out []catalog.Model
		for _, m := range catalog.Builtin("copilot") {
			if m.ID != "auto" {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

func (p Provider) accountFetched() (time.Time, bool) {
	accountLiveMu.Lock()
	defer accountLiveMu.Unlock()
	l, ok := accountLive[p.ID]
	return l.at, ok && len(l.models) > 0
}

func (p Provider) accountFetch(ctx context.Context) ([]catalog.Model, error) {
	switch p.Account.Agent {
	case CodexID:
		// Codex's list is built in; nothing to ask
		return append([]catalog.Model(nil), catalog.Builtin("codex")...), nil
	case CopilotID:
		ms, err := fetchCopilot(ctx)
		if err != nil {
			return nil, err
		}
		accountLiveMu.Lock()
		accountLive[p.ID] = liveList{models: ms, at: time.Now()}
		accountLiveMu.Unlock()
		return append([]catalog.Model(nil), ms...), nil
	}
	return nil, fmt.Errorf("%s has no model list", p.Name)
}

// Sign makes a request to the provider prove who it is: the API key for a
// provider the user added, the agent's own sign-in for an account. It may
// move the request to another URL (Copilot names its endpoint per session).
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case CodexID:
		return signCodex(ctx, req)
	case CopilotID:
		return signCopilot(ctx, p, req, body)
	}
	return fmt.Errorf("%s: unknown account kind %q", p.Name, p.Account.Agent)
}

// Prepare adjusts a request body to what the provider's backend accepts.
// Providers the user added get it back unchanged.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.Agent == CodexID {
		return prepareCodex(body)
	}
	return body
}

// MustStream reports whether the backend only answers with a stream.
func (p Provider) MustStream() bool { return p.Account != nil && p.Account.Agent == CodexID }

// SignInHint is what to tell the user when the provider rejects its
// credentials, or "" for providers the user added.
func (p Provider) SignInHint() string {
	if p.Account == nil {
		return ""
	}
	switch p.Account.Agent {
	case CodexID:
		return "signed out or token expired, please run `codex login` again (请重新登录)"
	case CopilotID:
		return "signed out or token no longer valid, please sign in to GitHub Copilot again (请重新登录)"
	}
	return ""
}

// ---- exclusions ----------------------------------------------------------------

var (
	keychainMu   sync.Mutex
	keychainAt   time.Time
	keychainHave bool
)

// claudeKeychain reports whether macOS's keychain holds a Claude Code item.
// It asks only whether the item exists (no -w/-g), so the secret is never
// read and no prompt appears. The answer is kept for a minute.
func claudeKeychain() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	keychainMu.Lock()
	defer keychainMu.Unlock()
	if !keychainAt.IsZero() && time.Since(keychainAt) < time.Minute {
		return keychainHave
	}
	cmd := exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	keychainHave = cmd.Run() == nil
	keychainAt = time.Now()
	return keychainHave
}

// claudeSignedIn reports whether Claude Code has a sign-in. Files are only
// checked for existence; their contents are never read.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	custom := dir != ""
	if !custom {
		dir = filepath.Join(homeDir(), ".claude")
	}
	for _, name := range []string{".credentials.json", "credentials.json"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return true
		}
	}
	return !custom && claudeKeychain()
}

// Excluded lists the sign-ins dial recognises but does not offer, and why.
func Excluded() []Exclusion {
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: ClaudeWhy})
	}
	return out
}
