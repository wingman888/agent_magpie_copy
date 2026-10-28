package provider

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

// Some agents carry a model subscription of their own: Codex signed in with
// ChatGPT, Copilot signed in with GitHub. dial lends those to the other
// agents as providers. The credentials stay in the agent's own files and are
// read at the moment of use; dial's file only keeps which models to expose.
// Sign out in the agent and the provider is gone.

// Account marks a provider that is an agent's own sign-in.
type Account struct {
	Agent  string `json:"agent"`          // "codex" or "copilot"
	User   string `json:"user"`           // email or GitHub login
	Plan   string `json:"plan,omitempty"` // subscription plan, when the sign-in says
	Stream bool   `json:"-"`              // the backend only streams
}

// Exclusion is a sign-in dial found but does not offer.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// Replaceable in tests.
var (
	CodexBase       = "https://chatgpt.com/backend-api/codex"
	CodexTokenURL   = "https://auth.openai.com/oauth/token"
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	copilotBase    = "https://api.githubcopilot.com"
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexRefreshAt = 5 * time.Minute // refresh when the token has less life than this
	copilotMargin  = 2 * time.Minute // reuse a session token while it has more life than this
)

// Accounts lists the providers of the agents signed in right now.
func Accounts() []Provider {
	var out []Provider
	if p, ok := codexAccount(); ok {
		out = append(out, p)
	}
	if p, ok := copilotAccount(); ok {
		out = append(out, p)
	}
	return out
}

func account(id string) (Provider, bool) {
	for _, p := range Accounts() {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// jwtClaims reads the payload of a JWT without checking its signature; the
// agent that holds the token is the one that trusts it.
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

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func openaiAuth(claims map[string]any) map[string]any {
	m, _ := claims["https://api.openai.com/auth"].(map[string]any)
	return m
}

// ---- Codex ------------------------------------------------------------------

type codexTokens struct {
	Mode      string
	ID        string
	Access    string
	Refresh   string
	AccountID string
}

func codexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readCodex reads Codex's sign-in from disk; ok is false when it is signed
// out or uses an API key (which dial has no business with).
func readCodex() (codexTokens, bool) {
	b, err := os.ReadFile(codexPath())
	if err != nil {
		return codexTokens{}, false
	}
	var f struct {
		Mode   string `json:"auth_mode"`
		Tokens struct {
			ID        string `json:"id_token"`
			Access    string `json:"access_token"`
			Refresh   string `json:"refresh_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &f) != nil || f.Tokens.Access == "" || strings.EqualFold(f.Mode, "apikey") {
		return codexTokens{}, false
	}
	return codexTokens{Mode: f.Mode, ID: f.Tokens.ID, Access: f.Tokens.Access, Refresh: f.Tokens.Refresh, AccountID: f.Tokens.AccountID}, true
}

func codexAccount() (Provider, bool) {
	t, ok := readCodex()
	if !ok {
		return Provider{}, false
	}
	claims := jwtClaims(t.ID)
	user := str(claims, "email")
	if user == "" {
		user = "ChatGPT"
	}
	return Provider{
		ID: "codex", Name: "Codex", Icon: "codex-color",
		Responses: strings.TrimRight(CodexBase, "/"),
		Account:   &Account{Agent: "codex", User: user, Plan: str(openaiAuth(claims), "chatgpt_plan_type"), Stream: true},
	}, true
}

var codexMu sync.Mutex // one refresh at a time

// expiresIn is how long a JWT has left; ok is false when it carries no exp.
func expiresIn(tok string) (time.Duration, bool) {
	exp, ok := jwtClaims(tok)["exp"].(float64)
	if !ok {
		return 0, false
	}
	return time.Until(time.Unix(int64(exp), 0)), true
}

func codexStale(t codexTokens) bool {
	left, ok := expiresIn(t.Access)
	return ok && left < codexRefreshAt
}

var errCodexOut = errors.New("Codex is signed out; run codex login")

func (p Provider) signCodex(ctx context.Context, req *http.Request) error {
	t, ok := readCodex()
	if !ok {
		return errCodexOut
	}
	if codexStale(t) {
		var err error
		if t, err = refreshCodex(ctx); err != nil {
			return errors.New("Codex is signed out (token refresh failed); run codex login")
		}
	}
	id := t.AccountID
	if id == "" {
		id = str(openaiAuth(jwtClaims(t.ID)), "chatgpt_account_id")
	}
	req.Header.Set("Authorization", "Bearer "+t.Access)
	if id != "" {
		req.Header.Set("chatgpt-account-id", id)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "dial")
	return nil
}

// refreshCodex trades the refresh token for new ones and writes them back
// into Codex's own file, where Codex will find them.
func refreshCodex(ctx context.Context) (codexTokens, error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	t, ok := readCodex()
	if !ok {
		return t, errCodexOut
	}
	if !codexStale(t) { // someone else refreshed while we waited
		return t, nil
	}
	if t.Refresh == "" {
		return t, errors.New("no refresh token")
	}
	body, _ := json.Marshal(map[string]string{
		"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": t.Refresh, "scope": "openid profile email",
	})
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexTokenURL, bytes.NewReader(body))
	if err != nil {
		return t, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return t, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return t, fmt.Errorf("token endpoint: %s", res.Status)
	}
	var out struct {
		Access  string `json:"access_token"`
		ID      string `json:"id_token"`
		Refresh string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &out) != nil || out.Access == "" {
		return t, errors.New("token endpoint sent no access token")
	}
	if err := writeCodexTokens(out.Access, out.ID, out.Refresh); err != nil {
		return t, err
	}
	t.Access = out.Access
	if out.ID != "" {
		t.ID = out.ID
	}
	if out.Refresh != "" {
		t.Refresh = out.Refresh
	}
	return t, nil
}

// writeCodexTokens updates the tokens in auth.json and leaves the rest of
// the file as it was.
func writeCodexTokens(access, id, refresh string) error {
	path := codexPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	tokens, _ := m["tokens"].(map[string]any)
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
	m["tokens"] = tokens
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".dial.tmp"
	if err := os.WriteFile(tmp, append(out, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// codexBody makes a Responses request acceptable to the ChatGPT backend:
// it streams only, keeps nothing, and refuses several common parameters.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	for _, k := range []string{"max_output_tokens", "max_completion_tokens", "temperature", "top_p",
		"previous_response_id", "user", "safety_identifier", "service_tier"} {
		delete(m, k)
	}
	m["store"], m["stream"] = false, true
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

// ---- Copilot ----------------------------------------------------------------

type copilotLogin struct{ User, OAuth string }

func copilotDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "github-copilot")
}

// readCopilot finds the GitHub sign-in Copilot keeps in apps.json (or the
// older hosts.json): an object keyed "github.com:<app id>".
func readCopilot() (copilotLogin, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var m map[string]struct {
			User  string `json:"user"`
			OAuth string `json:"oauth_token"`
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		var keys []string
		for k, v := range m {
			if strings.HasPrefix(k, "github.com") && v.OAuth != "" {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			continue
		}
		sort.Strings(keys)
		v := m[keys[0]]
		if v.User == "" {
			v.User = "GitHub"
		}
		return copilotLogin{User: v.User, OAuth: v.OAuth}, true
	}
	return copilotLogin{}, false
}

func copilotAccount() (Provider, bool) {
	l, ok := readCopilot()
	if !ok {
		return Provider{}, false
	}
	return Provider{
		ID: "copilot", Name: "Copilot", Icon: "githubcopilot",
		Chat:    copilotBase,
		Account: &Account{Agent: "copilot", User: l.User},
	}, true
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

func copilotSignedOut(why string) error {
	return fmt.Errorf("Copilot is signed out (%s); sign in to Copilot again", why)
}

// copilotEditor sets the headers that make the request look like Copilot
// Chat in an editor, which is what the service expects to talk to.
func copilotEditor(h http.Header) {
	h.Set("Editor-Version", "vscode/1.104.0")
	h.Set("Editor-Plugin-Version", "copilot-chat/0.31.0")
	h.Set("Copilot-Integration-Id", "vscode-chat")
	h.Set("User-Agent", "GitHubCopilotChat/0.31.0")
	h.Set("Openai-Intent", "conversation-panel")
}

// copilotToken trades the GitHub token for a short-lived session token,
// reusing the last one while it has life left.
func copilotToken(ctx context.Context) (copilotSession, error) {
	l, ok := readCopilot()
	if !ok {
		return copilotSession{}, copilotSignedOut("no GitHub sign-in found")
	}
	key := l.OAuth + "\x00" + CopilotTokenURL
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if s, ok := copilotSessions[key]; ok && time.Until(s.expires) > copilotMargin {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, copilotSignedOut(err.Error())
	}
	req.Header.Set("Authorization", "token "+l.OAuth)
	req.Header.Set("Accept", "application/json")
	copilotEditor(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, copilotSignedOut(err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return copilotSession{}, copilotSignedOut(APIError(b, res.Status))
	}
	var v struct {
		Token     string          `json:"token"`
		ExpiresAt json.RawMessage `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSession{}, copilotSignedOut("no session token")
	}
	s := copilotSession{token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/"), expires: parseExpiry(v.ExpiresAt)}
	copilotSessions[key] = s
	return s, nil
}

// parseExpiry reads expires_at, which GitHub sends as Unix seconds.
func parseExpiry(raw json.RawMessage) time.Time {
	var n float64
	if json.Unmarshal(raw, &n) == nil && n > 0 {
		return time.Unix(int64(n), 0)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (p Provider) signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	s, err := copilotToken(ctx)
	if err != nil {
		return err
	}
	if s.api != "" {
		if u, err := url.Parse(s.api); err == nil && u.Host != "" {
			req.URL.Scheme, req.URL.Host, req.Host = u.Scheme, u.Host, u.Host
		}
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	copilotEditor(req.Header)
	req.Header.Set("X-Initiator", copilotInitiator(body))
	if bytes.Contains(body, []byte(`"image_url"`)) {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	return nil
}

// copilotInitiator tells Copilot whether a person or the agent loop sent
// this turn: a turn that answers tool output is the agent's.
func copilotInitiator(body []byte) string {
	var v struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) == nil && len(v.Messages) > 0 && v.Messages[len(v.Messages)-1].Role == "user" {
		return "user"
	}
	return "agent"
}

// Names Copilot lists for its own plumbing, not for people to pick.
var copilotInternalPrefix = []string{"copilot-search", "exec-agent", "trajectory"}
var copilotInternalSuffix = []string{"-secondary", "-tertiary", "-4th", "-free-auto"}

func copilotInternal(id string) bool {
	for _, s := range copilotInternalPrefix {
		if strings.HasPrefix(id, s) {
			return true
		}
	}
	for _, s := range copilotInternalSuffix {
		if strings.HasSuffix(id, s) {
			return true
		}
	}
	return false
}

func (p Provider) fetchCopilot(ctx context.Context) ([]catalog.Model, error) {
	s, err := copilotToken(ctx)
	if err != nil {
		return nil, err
	}
	base := s.api
	if base == "" {
		base = copilotBase
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	copilotEditor(req.Header)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Copilot models: %s", APIError(b, res.Status))
	}
	var v struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			PickerEnabled bool   `json:"model_picker_enabled"`
			Policy        struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					Efforts []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("Copilot models: not a model list")
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range v.Data {
		if m.Capabilities.Type != "chat" || m.ID == "" || seen[m.ID] || copilotInternal(m.ID) {
			continue
		}
		if !m.PickerEnabled && m.Policy.State != "enabled" {
			continue
		}
		seen[m.ID] = true
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name, Efforts: m.Capabilities.Supports.Efforts})
	}
	if len(out) == 0 {
		return nil, errors.New("Copilot lists no chat model for this account")
	}
	return out, catalog.SaveLive(p.ID, base, out)
}

// accountModels is what an account is known to serve before any fetch.
func (p Provider) accountModels() []catalog.Model {
	switch p.Account.Agent {
	case "codex":
		return catalog.Builtin("codex")
	case "copilot":
		if live, _, ok := catalog.Live(p.ID); ok {
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
	return nil
}

// ---- signing ----------------------------------------------------------------

// Sign proves who is asking: the vendor's API key, or for an agent's account
// the agent's own token. body is the request body, for the headers Copilot
// derives from it.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account != nil {
		switch p.Account.Agent {
		case "codex":
			return p.signCodex(ctx, req)
		case "copilot":
			return p.signCopilot(ctx, req, body)
		}
	}
	for k, v := range AuthHeaders(p, proto) {
		req.Header.Set(k, v)
	}
	return nil
}

// Prepare adjusts a request body to what the backend accepts.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" {
		return codexBody(body)
	}
	return body
}

// ---- Claude Code ------------------------------------------------------------

const claudeWhy = "Anthropic's terms only allow a Claude subscription to be used by Claude Code itself, " +
	"so dial does not lend it to other agents; add an Anthropic API key as a provider instead (dial provider add anthropic <key>)"

var (
	keychainMu    sync.Mutex
	keychainAt    time.Time
	keychainFound bool
)

// claudeKeychain reports whether macOS holds a Claude Code item. It asks
// for the item's existence only, never its secret, and remembers the answer
// for a minute since the lookup starts a process.
func claudeKeychain() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	keychainMu.Lock()
	defer keychainMu.Unlock()
	if !keychainAt.IsZero() && time.Since(keychainAt) < time.Minute {
		return keychainFound
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	keychainFound = exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials").Run() == nil
	keychainAt = time.Now()
	return keychainFound
}

// Excluded lists the sign-ins dial found but does not offer. Only the
// existence of Claude Code's credentials is checked, never their content.
func Excluded() []Exclusion {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	if _, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil || claudeKeychain() {
		return []Exclusion{{Agent: "claude", Why: claudeWhy}}
	}
	return nil
}
