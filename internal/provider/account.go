package provider

// Account providers: models reached through a login another agent already
// holds (Codex with ChatGPT, GitHub Copilot). dial never stores those
// credentials; the agent's own files are read on every use.

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
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account says whose login an account provider borrows. No secrets.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
}

// Exclusion is a login dial recognises but deliberately does not offer.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// Endpoints dial talks to; variables so tests can point them elsewhere.
var (
	CodexBase         = codexDefaultBase
	CodexRefreshURL   = codexDefaultRefresh
	CopilotTokenURL   = "https://api.github.com/copilot_internal/v2/token"
	copilotDefaultAPI = "https://api.githubcopilot.com"
)

const (
	codexDefaultBase    = "https://chatgpt.com/backend-api/codex"
	codexDefaultRefresh = "https://auth.openai.com/oauth/token"
	codexClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexSignedOut      = "Codex is signed out; run `codex login` again"
	copilotOut          = "Copilot is signed out; sign in to GitHub Copilot again"
)

// SignedOutError means the agent's login is gone or no longer works.
type SignedOutError struct{ Msg string }

func (e *SignedOutError) Error() string { return e.Msg }

// SignedOutMessage is what to tell the user when an account provider's
// login stops working.
func (p Provider) SignedOutMessage() string {
	if p.Account != nil && p.Account.Agent == "copilot" {
		return copilotOut
	}
	return codexSignedOut
}

// IsAccountID reports whether an id belongs to an account provider.
func IsAccountID(id string) bool { return id == "codex" || id == "copilot" }

func homeDir() string { h, _ := os.UserHomeDir(); return h }

func codexAuthPath() string { return filepath.Join(homeDir(), ".codex", "auth.json") }

func copilotDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	return filepath.Join(homeDir(), ".config", "github-copilot")
}

// ---- reading logins -----------------------------------------------------------

type codexLogin struct {
	Access, Refresh, ID, AccountID string
	Email, Plan                    string
	Exp                            time.Time
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

func str(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func readCodexRaw() (map[string]any, error) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func codexFrom(m map[string]any) (codexLogin, bool) {
	var l codexLogin
	if mode := strings.ToLower(str(m, "auth_mode")); mode == "apikey" || mode == "api_key" {
		return l, false
	}
	t, _ := m["tokens"].(map[string]any)
	l.Access, l.Refresh, l.ID, l.AccountID = str(t, "access_token"), str(t, "refresh_token"), str(t, "id_token"), str(t, "account_id")
	if l.Access == "" {
		return l, false
	}
	for _, tok := range []string{l.ID, l.Access} {
		c := jwtClaims(tok)
		if c == nil {
			continue
		}
		if l.Email == "" {
			l.Email = str(c, "email")
			if l.Email == "" {
				if pr, ok := c["https://api.openai.com/profile"].(map[string]any); ok {
					l.Email = str(pr, "email")
				}
			}
		}
		if au, ok := c["https://api.openai.com/auth"].(map[string]any); ok {
			if l.Plan == "" {
				l.Plan = str(au, "chatgpt_plan_type")
			}
			if l.AccountID == "" {
				l.AccountID = str(au, "chatgpt_account_id")
			}
		}
	}
	if c := jwtClaims(l.Access); c != nil {
		if exp, ok := c["exp"].(float64); ok {
			l.Exp = time.Unix(int64(exp), 0)
		}
	}
	return l, true
}

func readCodex() (codexLogin, bool) {
	m, err := readCodexRaw()
	if err != nil {
		return codexLogin{}, false
	}
	return codexFrom(m)
}

type copilotLogin struct{ OAuth, User string }

func readCopilot() (copilotLogin, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var m map[string]map[string]any
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		for k, v := range m {
			if strings.HasPrefix(k, "github.com") {
				if tok := str(v, "oauth_token"); tok != "" {
					return copilotLogin{OAuth: tok, User: str(v, "user")}, true
				}
			}
		}
	}
	return copilotLogin{}, false
}

// Accounts lists the account providers whose agent is signed in now.
func Accounts() []Provider {
	var out []Provider
	if l, ok := readCodex(); ok {
		user := l.Email
		if user == "" {
			user = "ChatGPT"
		}
		out = append(out, Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Responses: CodexBase,
			Account: &Account{Agent: "codex", User: user, Plan: l.Plan}})
	}
	if l, ok := readCopilot(); ok {
		user := l.User
		if user == "" {
			user = "GitHub"
		}
		out = append(out, Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Chat: copilotDefaultAPI,
			Account: &Account{Agent: "copilot", User: user}})
	}
	return out
}

// Label is what a model's note shows: the signed-in user for accounts,
// else the provider's name.
func (p Provider) Label() string {
	if p.Account != nil && p.Account.User != "" {
		return p.Account.User
	}
	return p.Name
}

// ---- excluded logins ------------------------------------------------------------

var (
	exMu    sync.Mutex
	exKey   string
	exAt    time.Time
	exCache []Exclusion
)

func resetExcluded() { exMu.Lock(); exKey, exAt, exCache = "", time.Time{}, nil; exMu.Unlock() }

// Excluded lists logins dial recognises but will not offer. It only checks
// that a Claude Code login exists; it never reads one.
func Excluded() []Exclusion {
	key := homeDir() + "\x00" + os.Getenv("CLAUDE_CONFIG_DIR")
	exMu.Lock()
	defer exMu.Unlock()
	if exKey == key && time.Since(exAt) < time.Minute {
		return append([]Exclusion(nil), exCache...)
	}
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: "Anthropic's terms only allow a Claude subscription to be used with Claude Code itself — add an Anthropic API key as a provider instead (dial provider add anthropic <key>)"})
	}
	exKey, exAt, exCache = key, time.Now(), out
	return append([]Exclusion(nil), out...)
}

func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(homeDir(), ".claude")
	}
	if st, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil && !st.IsDir() {
		return true
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// existence only: no -w / -g, so neither the secret nor a prompt
		return exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials").Run() == nil
	}
	return false
}

// ---- Prepare ---------------------------------------------------------------------

// Prepare adjusts a request body to what the backend accepts. Ordinary
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
	for _, k := range []string{"max_output_tokens", "max_tokens", "max_completion_tokens", "temperature", "top_p",
		"presence_penalty", "frequency_penalty", "stream_options", "previous_response_id", "metadata",
		"truncation", "service_tier", "user", "top_logprobs", "seed"} {
		delete(m, k)
	}
	m["stream"] = true
	m["store"] = false
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

// ---- Sign ------------------------------------------------------------------------

// Sign makes req prove who it is to this provider.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		return signCodex(ctx, req)
	case "copilot":
		return signCopilot(ctx, req, body)
	}
	return fmt.Errorf("unknown account agent %q", p.Account.Agent)
}

var codexMu sync.Mutex

func (l codexLogin) expiring() bool {
	return !l.Exp.IsZero() && time.Until(l.Exp) < 5*time.Minute
}

func codexToken(ctx context.Context) (codexLogin, error) {
	out := &SignedOutError{codexSignedOut}
	l, ok := readCodex()
	if !ok {
		return l, out
	}
	if !l.expiring() {
		return l, nil
	}
	codexMu.Lock()
	defer codexMu.Unlock()
	m, err := readCodexRaw()
	if err != nil {
		return l, out
	}
	l, ok = codexFrom(m)
	if !ok {
		return l, out
	}
	if !l.expiring() { // someone else refreshed while we waited
		return l, nil
	}
	if l.Refresh == "" {
		return l, out
	}
	return refreshCodex(ctx, m, l)
}

// codexRefreshEndpoint resolves the token refresh URL. If CodexRefreshURL is
// untouched but CodexBase was redirected (test double), refresh goes to the
// same scheme://host as CodexBase.
func codexRefreshEndpoint() string {
	if CodexRefreshURL == codexDefaultRefresh && CodexBase != codexDefaultBase {
		if u, err := url.Parse(CodexBase); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host + "/oauth/token"
		}
	}
	return CodexRefreshURL
}

func refreshCodex(ctx context.Context, m map[string]any, l codexLogin) (codexLogin, error) {
	out := &SignedOutError{codexSignedOut}
	payload, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": l.Refresh, "scope": "openid profile email"})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexRefreshEndpoint(), bytes.NewReader(payload))
	if err != nil {
		return l, out
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return l, &SignedOutError{codexSignedOut + " (token refresh failed: " + err.Error() + ")"}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return l, &SignedOutError{fmt.Sprintf("%s (token refresh failed: %s)", codexSignedOut, res.Status)}
	}
	var nt struct {
		Access  string `json:"access_token"`
		ID      string `json:"id_token"`
		Refresh string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &nt) != nil || nt.Access == "" {
		return l, out
	}
	toks, _ := m["tokens"].(map[string]any)
	if toks == nil {
		toks = map[string]any{}
		m["tokens"] = toks
	}
	toks["access_token"] = nt.Access
	if nt.ID != "" {
		toks["id_token"] = nt.ID
	}
	if nt.Refresh != "" {
		toks["refresh_token"] = nt.Refresh
	}
	m["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeJSONAtomic(codexAuthPath(), m); err != nil {
		return l, fmt.Errorf("could not save the refreshed Codex token: %w", err)
	}
	nl, ok := codexFrom(m)
	if !ok {
		return l, out
	}
	return nl, nil
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".auth-*.tmp")
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
	return os.Rename(tmp, path)
}

func signCodex(ctx context.Context, req *http.Request) error {
	l, err := codexToken(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+l.Access)
	if l.AccountID != "" {
		req.Header.Set("chatgpt-account-id", l.AccountID)
	}
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	return nil
}

// ---- Copilot ---------------------------------------------------------------------

type session struct {
	token, api string
	expires    time.Time
}

var (
	sessMu   sync.Mutex
	sessions = map[string]session{} // by GitHub token; in memory only
)

func copilotHeaders(h http.Header) {
	h.Set("Editor-Version", "vscode/1.100.0")
	h.Set("Editor-Plugin-Version", "copilot-chat/0.30.0")
	h.Set("Copilot-Integration-Id", "vscode-chat")
	h.Set("User-Agent", "GitHubCopilotChat/0.30.0")
}

func copilotSession(ctx context.Context) (session, error) {
	l, ok := readCopilot()
	if !ok {
		return session{}, &SignedOutError{copilotOut}
	}
	sessMu.Lock()
	defer sessMu.Unlock()
	key := CopilotTokenURL + "\x00" + l.OAuth
	if s, ok := sessions[key]; ok && time.Until(s.expires) > 60*time.Second {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return session{}, err
	}
	copilotHeaders(req.Header)
	req.Header.Set("Authorization", "token "+l.OAuth)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return session{}, fmt.Errorf("Copilot token exchange: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404 {
		delete(sessions, key)
		return session{}, &SignedOutError{fmt.Sprintf("%s (token exchange: %s)", copilotOut, res.Status)}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return session{}, fmt.Errorf("Copilot token exchange: %s", APIError(b, res.Status))
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return session{}, errors.New("Copilot token exchange: unexpected reply")
	}
	s := session{token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/"), expires: time.Unix(v.ExpiresAt, 0)}
	if s.api == "" {
		s.api = copilotDefaultAPI
	}
	if v.ExpiresAt == 0 {
		s.expires = time.Now().Add(10 * time.Minute)
	}
	sessions[key] = s
	return s, nil
}

func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	s, err := copilotSession(ctx)
	if err != nil {
		return err
	}
	if u, err := url.Parse(s.api); err == nil && u.Host != "" {
		req.URL.Scheme, req.URL.Host, req.Host = u.Scheme, u.Host, u.Host
	}
	copilotHeaders(req.Header)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Openai-Intent", "conversation-edits")
	if len(body) > 0 {
		initiator, vision := "user", false
		var v struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &v) == nil {
			if n := len(v.Messages); n > 0 && v.Messages[n-1].Role != "user" {
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
		}
		req.Header.Set("X-Initiator", initiator)
		if vision {
			req.Header.Set("Copilot-Vision-Request", "true")
		}
	}
	return nil
}

// ---- models ----------------------------------------------------------------------

var (
	liveMu   sync.Mutex
	copilotM []catalog.Model
	copilotT time.Time
	copilotK string // CopilotTokenURL + NUL + oauth token the list belongs to
)

func copilotKey() (string, bool) {
	l, ok := readCopilot()
	if !ok {
		return "", false
	}
	return CopilotTokenURL + "\x00" + l.OAuth, true
}

func accountLive(p Provider) ([]catalog.Model, time.Time, bool) {
	if p.Account != nil && p.Account.Agent == "copilot" {
		k, ok := copilotKey()
		if !ok {
			return nil, time.Time{}, false
		}
		liveMu.Lock()
		defer liveMu.Unlock()
		if copilotM != nil && copilotK == k {
			return copilotM, copilotT, true
		}
	}
	return nil, time.Time{}, false
}

func (p Provider) accountAvailable() []catalog.Model {
	if p.Account.Agent == "codex" {
		return catalog.Builtin("codex")
	}
	if ms, _, ok := accountLive(p); ok {
		return ms
	}
	var out []catalog.Model
	for _, m := range catalog.Builtin("copilot") {
		if m.ID != "auto" {
			out = append(out, m)
		}
	}
	return out
}

func (p Provider) accountFetch(ctx context.Context) ([]catalog.Model, error) {
	if p.Account.Agent == "codex" {
		return catalog.Builtin("codex"), nil
	}
	s, err := copilotSession(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	if err := p.Sign(ctx, req, Chat, nil); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == 401 {
		return nil, &SignedOutError{copilotOut}
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, errors.New(APIError(b, res.Status))
	}
	var v struct {
		Data []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled *bool  `json:"model_picker_enabled"`
			Capabilities       struct {
				Type     string `json:"type"`
				Supports struct {
					ReasoningEffort []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("Copilot model list: %w", err)
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range v.Data {
		if m.ID == "" || m.ID == "auto" || seen[m.ID] || m.Capabilities.Type != "chat" {
			continue
		}
		if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
			continue
		}
		if m.Policy != nil && m.Policy.State != "" && m.Policy.State != "enabled" {
			continue
		}
		seen[m.ID] = true
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name, Efforts: m.Capabilities.Supports.ReasoningEffort})
	}
	if len(out) == 0 {
		return nil, errors.New("Copilot lists no chat models for this account")
	}
	k, _ := copilotKey()
	liveMu.Lock()
	copilotM, copilotT, copilotK = out, time.Now(), k
	liveMu.Unlock()
	return out, nil
}
