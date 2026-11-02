package provider

// Account providers: models that come with a subscription the user already
// signed in to inside another agent (Codex → ChatGPT, Copilot → GitHub).
// dial never stores those credentials. Every use reads the agent's own login
// file; the only thing dial keeps is which models the user ticked.

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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// CodexBase is the ChatGPT Codex backend; requests for codex/<model> go to
// CodexBase + "/responses". Tests point it at a fake server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CodexRefreshURL is where Codex trades a refresh token for new tokens.
var CodexRefreshURL = "https://auth.openai.com/oauth/token"

// GithubAPI is the GitHub API root used to mint Copilot session tokens.
var GithubAPI = "https://api.github.com"

const (
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	copilotDefault = "https://api.githubcopilot.com"
	// IDs of the account providers.
	CodexID   = "codex"
	CopilotID = "copilot"
	claudeID  = "claude"
)

// Account describes the login behind an account provider.
type Account struct {
	Kind  string `json:"kind"`  // "codex" | "copilot"
	User  string `json:"user"`  // email (Codex) or GitHub login (Copilot)
	Plan  string `json:"plan"`  // subscription plan, when known
	Agent string `json:"agent"` // the agent the login comes from
}

// IsAccount reports whether the provider is backed by an agent login.
func (p Provider) IsAccount() bool { return p.Account != nil }

// IsAccountID reports whether id names an account provider (signed in or not).
func IsAccountID(id string) bool { return id == CodexID || id == CopilotID }

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

// ---- reading the agents' login files -------------------------------------

func codexAuthPath() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
		return filepath.Join(d, "auth.json")
	}
	return filepath.Join(home(), ".codex", "auth.json")
}

type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

type codexAuth struct {
	Mode   string      `json:"auth_mode"`
	APIKey string      `json:"OPENAI_API_KEY"`
	Tokens codexTokens `json:"tokens"`
}

func readCodexAuth() (codexAuth, error) {
	var a codexAuth
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	switch {
	case a.Mode == "chatgpt":
	case a.Mode == "" && a.APIKey == "" && a.Tokens.AccessToken != "":
	default:
		return a, errors.New("codex is not signed in with ChatGPT")
	}
	if a.Tokens.AccessToken == "" {
		return a, errors.New("codex login has no access token")
	}
	return a, nil
}

// jwtClaims decodes the payload of a JWT without verifying it.
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

func jwtExp(tok string) time.Time {
	if f, ok := jwtClaims(tok)["exp"].(float64); ok {
		return time.Unix(int64(f), 0)
	}
	return time.Time{}
}

func codexAccount() *Account {
	a, err := readCodexAuth()
	if err != nil {
		return nil
	}
	acc := &Account{Kind: CodexID, Agent: "Codex"}
	c := jwtClaims(a.Tokens.IDToken)
	acc.User, _ = c["email"].(string)
	if auth, ok := c["https://api.openai.com/auth"].(map[string]any); ok {
		acc.Plan, _ = auth["chatgpt_plan_type"].(string)
	}
	if acc.User == "" {
		acc.User = "ChatGPT"
	}
	return acc
}

func copilotAppsPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot", "apps.json")
	}
	return filepath.Join(home(), ".config", "github-copilot", "apps.json")
}

// copilotLogin returns the GitHub login and OAuth token Copilot stored.
func copilotLogin() (user, token string, ok bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return "", "", false
	}
	var m map[string]struct {
		User  string `json:"user"`
		Token string `json:"oauth_token"`
	}
	if json.Unmarshal(b, &m) != nil {
		return "", "", false
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if strings.HasPrefix(k, "github.com") && m[k].Token != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "", "", false
	}
	sort.Strings(keys)
	e := m[keys[0]]
	return e.User, e.Token, true
}

func copilotAccount() *Account {
	user, _, ok := copilotLogin()
	if !ok {
		return nil
	}
	return &Account{Kind: CopilotID, User: user, Agent: "Copilot"}
}

// Withheld is a signed-in agent whose login dial deliberately does not offer.
type Withheld struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// ClaudeReason is why Claude Code's login is never offered as a provider.
const ClaudeReason = "Anthropic does not allow Claude subscription logins to be used by other tools or gateways; use an Anthropic API key instead"

// Withheld lists agents that are signed in but not provided. dial only checks
// that the Claude Code credentials file exists; it never reads it.
func WithheldAccounts() []Withheld {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(home(), ".claude")
	}
	if st, err := os.Stat(filepath.Join(dir, ".credentials.json")); err != nil || st.IsDir() {
		return nil
	}
	return []Withheld{{ID: claudeID, Name: "Claude Code", Reason: ClaudeReason}}
}

// accountProviders builds the providers for the agents currently signed in.
func accountProviders() []Provider {
	var out []Provider
	if a := codexAccount(); a != nil {
		out = append(out, Provider{ID: CodexID, Name: "Codex", Icon: "codex-color", Catalog: "openai",
			Responses: CodexBase, Account: a, Website: "https://chatgpt.com/codex"})
	}
	if a := copilotAccount(); a != nil {
		out = append(out, Provider{ID: CopilotID, Name: "Copilot", Icon: "githubcopilot",
			Chat: copilotDefault, Account: a, Catalog: "github-copilot", Website: "https://github.com/features/copilot"})
	}
	return out
}

// ---- default model lists --------------------------------------------------

var codexDefaults = []catalog.Model{
	{ID: "gpt-5.5", Name: "GPT-5.5", Efforts: []string{"low", "medium", "high", "xhigh"}},
	{ID: "gpt-5.4", Name: "GPT-5.4", Efforts: []string{"low", "medium", "high", "xhigh"}},
	{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Efforts: []string{"low", "medium", "high", "xhigh"}},
	{ID: "gpt-5.2", Name: "GPT-5.2", Efforts: []string{"low", "medium", "high", "xhigh"}},
}

var copilotDefaults = []catalog.Model{
	{ID: "gpt-4.1", Name: "GPT-4.1"},
	{ID: "gpt-4o", Name: "GPT-4o"},
	{ID: "gpt-5", Name: "GPT-5"},
	{ID: "gpt-5-mini", Name: "GPT-5 mini"},
	{ID: "claude-sonnet-4", Name: "Claude Sonnet 4"},
	{ID: "claude-sonnet-4.5", Name: "Claude Sonnet 4.5"},
	{ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro"},
}

func (p Provider) accountDefaults() []catalog.Model {
	if p.Account != nil && p.Account.Kind == CopilotID {
		return append([]catalog.Model(nil), copilotDefaults...)
	}
	return append([]catalog.Model(nil), codexDefaults...)
}

// ---- credentials for upstream calls ----------------------------------------

var refreshMu sync.Mutex

// ErrCodexLoggedOut is returned when Codex's tokens can no longer be used.
func codexLoggedOut(why string) error {
	msg := "Codex 已登出，请运行 `codex login` (Codex is signed out — run `codex login`)"
	if why != "" {
		msg += ": " + why
	}
	return errors.New(msg)
}

// codexToken returns a usable access token and account id, refreshing the
// login file first when the token expires within five minutes.
func codexToken(ctx context.Context) (token, accountID string, err error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	a, err := readCodexAuth()
	if err != nil {
		return "", "", codexLoggedOut("")
	}
	if exp := jwtExp(a.Tokens.AccessToken); !exp.IsZero() && time.Until(exp) < 5*time.Minute {
		if a.Tokens.RefreshToken == "" {
			return "", "", codexLoggedOut("no refresh token")
		}
		if a, err = refreshCodex(ctx, a); err != nil {
			return "", "", err
		}
	}
	return a.Tokens.AccessToken, a.Tokens.AccountID, nil
}

func refreshCodex(ctx context.Context, a codexAuth) (codexAuth, error) {
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": a.Tokens.RefreshToken, "scope": "openid profile email"})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return a, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return a, codexLoggedOut(err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return a, codexLoggedOut(APIError(b, res.Status))
	}
	var nt struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &nt) != nil || nt.AccessToken == "" {
		return a, codexLoggedOut("refresh returned no access token")
	}
	// write back, keeping every other field of the file as Codex wrote it
	path := codexAuthPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return a, codexLoggedOut(err.Error())
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return a, codexLoggedOut(err.Error())
	}
	var toks map[string]json.RawMessage
	json.Unmarshal(doc["tokens"], &toks)
	if toks == nil {
		toks = map[string]json.RawMessage{}
	}
	set := func(k, v string) {
		if v != "" {
			toks[k], _ = json.Marshal(v)
		}
	}
	set("id_token", nt.IDToken)
	set("access_token", nt.AccessToken)
	set("refresh_token", nt.RefreshToken)
	doc["tokens"], _ = json.Marshal(toks)
	doc["last_refresh"], _ = json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
	out, _ := json.MarshalIndent(doc, "", "  ")
	st, _ := os.Stat(path)
	mode := os.FileMode(0o600)
	if st != nil {
		mode = st.Mode().Perm()
	}
	tmp := path + ".dial-tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), mode); err != nil {
		return a, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return a, err
	}
	a.Tokens.AccessToken = nt.AccessToken
	if nt.IDToken != "" {
		a.Tokens.IDToken = nt.IDToken
	}
	if nt.RefreshToken != "" {
		a.Tokens.RefreshToken = nt.RefreshToken
	}
	return a, nil
}

type copilotSession struct {
	token, api string
	expires    time.Time
	gh         string
}

var (
	sessMu sync.Mutex
	sess   copilotSession
)

const (
	copilotEditor = "vscode/1.99.0"
	copilotPlugin = "copilot-chat/0.26.0"
)

// copilotSessionToken trades the GitHub token for a short-lived session
// token and the API base that goes with it. The session lives in memory only.
func copilotSessionToken(ctx context.Context) (token, api string, err error) {
	_, gh, ok := copilotLogin()
	if !ok {
		return "", "", errors.New("请重新登录 Copilot (Copilot is signed out — sign in again)")
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	if sess.gh == gh && sess.token != "" && time.Until(sess.expires) > time.Minute && sess.api != "" {
		return sess.token, sess.api, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(GithubAPI, "/")+"/copilot_internal/v2/token", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "token "+gh)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Editor-Version", copilotEditor)
	req.Header.Set("Editor-Plugin-Version", copilotPlugin)
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.0")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		sess = copilotSession{}
		if res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404 {
			return "", "", errors.New("请重新登录 Copilot (Copilot session token was refused — sign in again)")
		}
		return "", "", fmt.Errorf("copilot token: %s", APIError(b, res.Status))
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return "", "", errors.New("请重新登录 Copilot (no session token returned)")
	}
	api = strings.TrimRight(v.Endpoints.API, "/")
	if api == "" {
		api = copilotDefault
	}
	exp := time.Unix(v.ExpiresAt, 0)
	if v.ExpiresAt == 0 {
		exp = time.Now().Add(10 * time.Minute)
	}
	sess = copilotSession{token: v.Token, api: api, expires: exp, gh: gh}
	return v.Token, api, nil
}

// Upstream resolves how to call an account provider: the full URL for path,
// the headers that prove who the user is, and the body to send (rewritten for
// what the backend accepts). Credentials are read from the agent's login
// files on every call.
func (p Provider) Upstream(ctx context.Context, proto Protocol, path string, body []byte) (string, map[string]string, []byte, error) {
	if p.Account == nil {
		return p.Base(proto) + path, AuthHeaders(p, proto), body, nil
	}
	switch p.Account.Kind {
	case CodexID:
		tok, acct, err := codexToken(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		h := map[string]string{"Authorization": "Bearer " + tok, "OpenAI-Beta": "responses=experimental",
			"originator": "codex_cli_rs", "session_id": "dial"}
		if acct != "" {
			h["chatgpt-account-id"] = acct
		}
		return strings.TrimRight(CodexBase, "/") + path, h, CodexBody(body), nil
	case CopilotID:
		tok, api, err := copilotSessionToken(ctx)
		if err != nil {
			return "", nil, nil, err
		}
		h := copilotHeaders(tok, body)
		return api + path, h, body, nil
	}
	return "", nil, nil, errors.New("unknown account provider")
}

func copilotHeaders(tok string, body []byte) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + tok, "Editor-Version": copilotEditor,
		"Editor-Plugin-Version": copilotPlugin, "Copilot-Integration-Id": "vscode-chat",
		"User-Agent": "GitHubCopilotChat/0.26.0", "Openai-Intent": "conversation-edits",
		"X-Initiator": copilotInitiator(body)}
	if bytes.Contains(body, []byte(`"image_url"`)) || bytes.Contains(body, []byte(`"input_image"`)) ||
		bytes.Contains(body, []byte(`"type":"image"`)) {
		h["Copilot-Vision-Request"] = "true"
	}
	return h
}

// copilotInitiator is "user" when the last turn was typed by the user, and
// "agent" when it is a tool result going back (billed differently).
func copilotInitiator(body []byte) string {
	var v struct {
		Messages []json.RawMessage `json:"messages"`
		Input    json.RawMessage   `json:"input"`
	}
	if json.Unmarshal(body, &v) != nil {
		return "user"
	}
	items := v.Messages
	if len(items) == 0 && len(v.Input) > 0 {
		json.Unmarshal(v.Input, &items)
	}
	if len(items) == 0 {
		return "user"
	}
	last := items[len(items)-1]
	var m struct {
		Role    string          `json:"role"`
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(last, &m) != nil {
		return "user"
	}
	switch {
	case m.Role == "tool", m.Role == "assistant", m.Type == "function_call_output", m.Type == "function_call":
		return "agent"
	case m.Role == "user" && bytes.Contains(m.Content, []byte(`"tool_result"`)):
		return "agent"
	}
	return "user"
}

// CodexBody rewrites a Responses request for the ChatGPT Codex backend: it
// streams only, keeps no conversation, takes no sampling or length limits,
// and wants the input as a list of items.
func CodexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, CodexID+"/")
	}
	m["stream"], m["store"] = true, false
	for _, k := range []string{"max_output_tokens", "max_tokens", "max_completion_tokens", "temperature", "top_p",
		"presence_penalty", "frequency_penalty", "top_logprobs", "service_tier"} {
		delete(m, k)
	}
	switch in := m["input"].(type) {
	case string:
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": in}}}}
	case nil:
		m["input"] = []any{}
	}
	if _, ok := m["instructions"]; !ok {
		m["instructions"] = ""
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- fetching the live model lists -----------------------------------------

func (p Provider) fetchAccount(ctx context.Context) ([]catalog.Model, error) {
	var ms []catalog.Model
	var base string
	switch p.Account.Kind {
	case CodexID:
		tok, acct, err := codexToken(ctx)
		if err != nil {
			return nil, err
		}
		base = CodexBase
		h := map[string]string{"Authorization": "Bearer " + tok}
		if acct != "" {
			h["chatgpt-account-id"] = acct
		}
		b, err := getJSON(ctx, strings.TrimRight(CodexBase, "/")+"/models?client_version="+url.QueryEscape("1.0.0"), h)
		if err != nil {
			return nil, err
		}
		ms = parseCodexModels(b)
	case CopilotID:
		tok, api, err := copilotSessionToken(ctx)
		if err != nil {
			return nil, err
		}
		base = api
		h := copilotHeaders(tok, nil)
		b, err := getJSON(ctx, api+"/models", h)
		if err != nil {
			return nil, err
		}
		ms = parseCopilotModels(b)
	}
	if len(ms) == 0 {
		return nil, errors.New(p.Name + " returned no usable models")
	}
	return ms, catalog.SaveLive(p.ID, base, ms)
}

func getJSON(ctx context.Context, u string, h map[string]string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range h {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, APIError(b, res.Status))
	}
	return b, nil
}

func parseCodexModels(b []byte) []catalog.Model {
	var v struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Levels      []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []catalog.Model
	for _, m := range v.Models {
		if m.Slug == "" || m.Visibility == "hide" {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.Slug
		}
		cm := catalog.Model{ID: m.Slug, Name: name, Provider: "openai"}
		for _, l := range m.Levels {
			cm.Efforts = append(cm.Efforts, l.Effort)
		}
		out = append(out, cm)
	}
	return out
}

// parseCopilotModels keeps what the account can chat with: chat models only,
// nothing internal, nothing the account has not enabled, no "auto".
func parseCopilotModels(b []byte) []catalog.Model {
	var v struct {
		Data []struct {
			ID                string `json:"id"`
			Name              string `json:"name"`
			ModelPickerEnable *bool  `json:"model_picker_enabled"`
			Capabilities      struct {
				Type string `json:"type"`
			} `json:"capabilities"`
			Policy struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range v.Data {
		id := m.ID
		switch {
		case id == "" || seen[id], id == "auto", strings.Contains(strings.ToLower(id), "embedding"):
			continue
		case m.Capabilities.Type != "" && m.Capabilities.Type != "chat":
			continue
		case m.ModelPickerEnable != nil && !*m.ModelPickerEnable:
			continue
		case m.Policy.State != "" && m.Policy.State != "enabled":
			continue
		}
		seen[id] = true
		name := m.Name
		if name == "" {
			name = id
		}
		out = append(out, catalog.Model{ID: id, Name: name})
	}
	return out
}
