package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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

// Agent accounts as providers. Codex (ChatGPT sign-in) and Copilot (GitHub
// sign-in) keep their credentials in their own files; dial reads them every
// time it needs them and never stores a copy. Claude Code's sign-in is
// detected only, so the user can be told why it is not offered.

// Account describes the sign-in behind an account provider. No secrets.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
}

// Exclusion is a sign-in dial recognised but does not offer.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// Backends. Variables so tests can point them at stand-ins.
var (
	CodexBase         = "https://chatgpt.com/backend-api/codex"
	CodexRefreshURL   = "https://auth.openai.com/oauth/token"
	CopilotTokenURL   = "https://api.github.com/copilot_internal/v2/token"
	copilotDefaultAPI = "https://api.githubcopilot.com"
)

const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	copilotUA     = "GitHubCopilotChat/0.26.7"
	copilotEditor = "vscode/1.99.3"
	copilotPlugin = "copilot-chat/0.26.7"
	copilotIntID  = "vscode-chat"
)

// claudeWhy is the fixed explanation shown for the Claude Code sign-in.
const claudeWhy = "Anthropic's terms only allow a Claude subscription to be used with Claude Code itself, " +
	"so dial does not offer it as a provider. Use an Anthropic API key as a provider instead (dial provider add anthropic <key>)."

func isAccountID(id string) bool { return id == "codex" || id == "copilot" }

func userHome() string {
	h, _ := os.UserHomeDir()
	return h
}

// ---- JWT helpers -------------------------------------------------------------

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
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	return m
}

func claimString(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// ---- Codex ---------------------------------------------------------------------

type codexCreds struct {
	Access, Refresh, ID, AccountID string
}

var errCodexOut = errors.New("codex: signed out or token expired — run `codex login` again")

func codexAuthPath() string { return filepath.Join(userHome(), ".codex", "auth.json") }

func readCodexRaw() (map[string]any, error) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

func codexFromRaw(m map[string]any) (codexCreds, bool) {
	if mode, _ := m["auth_mode"].(string); strings.EqualFold(strings.TrimSpace(mode), "apikey") {
		return codexCreds{}, false
	}
	t, _ := m["tokens"].(map[string]any)
	if t == nil {
		return codexCreds{}, false
	}
	c := codexCreds{
		Access:    claimString(t, "access_token"),
		Refresh:   claimString(t, "refresh_token"),
		ID:        claimString(t, "id_token"),
		AccountID: claimString(t, "account_id"),
	}
	if c.Access == "" {
		return c, false
	}
	if c.AccountID == "" {
		if a, ok := jwtClaims(c.ID)["https://api.openai.com/auth"].(map[string]any); ok {
			c.AccountID = claimString(a, "chatgpt_account_id")
		}
	}
	return c, true
}

func readCodex() (codexCreds, bool) {
	m, err := readCodexRaw()
	if err != nil {
		return codexCreds{}, false
	}
	return codexFromRaw(m)
}

func codexAccount() (Provider, bool) {
	c, ok := readCodex()
	if !ok {
		return Provider{}, false
	}
	acc := Account{Agent: "codex", User: "ChatGPT"}
	claims := jwtClaims(c.ID)
	if e := claimString(claims, "email"); e != "" {
		acc.User = e
	}
	if a, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		acc.Plan = claimString(a, "chatgpt_plan_type")
	}
	return Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Catalog: "codex", Account: &acc}, true
}

var codexMu sync.Mutex

func tokenExpiresSoon(access string) bool {
	exp, ok := jwtClaims(access)["exp"].(json.Number)
	if !ok {
		return false
	}
	f, err := exp.Float64()
	if err != nil {
		return false
	}
	return time.Until(time.Unix(int64(f), 0)) < 5*time.Minute
}

func codexRefreshEndpoint() string {
	if u := os.Getenv("CODEX_REFRESH_TOKEN_URL_OVERRIDE"); u != "" {
		return u
	}
	return CodexRefreshURL
}

// codexToken returns a usable access token and account id, refreshing the
// token (and writing it back to Codex's own file) when it is about to expire.
func codexToken(ctx context.Context) (codexCreds, error) {
	c, ok := readCodex()
	if !ok {
		return c, errCodexOut
	}
	if !tokenExpiresSoon(c.Access) {
		return c, nil
	}
	codexMu.Lock()
	defer codexMu.Unlock()
	raw, err := readCodexRaw()
	if err != nil {
		return c, errCodexOut
	}
	c, ok = codexFromRaw(raw)
	if !ok {
		return c, errCodexOut
	}
	if !tokenExpiresSoon(c.Access) {
		return c, nil // another request refreshed it while we waited
	}
	if c.Refresh == "" {
		return c, errCodexOut
	}
	body, _ := json.Marshal(map[string]string{
		"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": c.Refresh, "scope": "openid profile email",
	})
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, codexRefreshEndpoint(), bytes.NewReader(body))
	if err != nil {
		return c, errCodexOut
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return c, fmt.Errorf("codex: token refresh failed (%v) — if this persists, run `codex login` again", err)
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return c, fmt.Errorf("codex: signed out or token expired (refresh answered %s) — run `codex login` again", res.Status)
	}
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		ID      string `json:"id_token"`
	}
	if json.Unmarshal(rb, &out) != nil || out.Access == "" {
		return c, errCodexOut
	}
	tokens, _ := raw["tokens"].(map[string]any)
	if tokens == nil {
		tokens = map[string]any{}
		raw["tokens"] = tokens
	}
	tokens["access_token"] = out.Access
	c.Access = out.Access
	if out.ID != "" {
		tokens["id_token"] = out.ID
		c.ID = out.ID
	}
	if out.Refresh != "" {
		tokens["refresh_token"] = out.Refresh
		c.Refresh = out.Refresh
	}
	if err := writeJSONAtomic(codexAuthPath(), raw); err != nil {
		return c, fmt.Errorf("codex: refreshed the token but could not save it: %w", err)
	}
	return c, nil
}

// writeJSONAtomic replaces a file keeping its permissions (0600 if new).
func writeJSONAtomic(path string, v any) error {
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Prepare adjusts a request body to what the backend demands.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil || p.Account.Agent != "codex" {
		return body
	}
	return prepareCodex(body)
}

// Fields the ChatGPT backend refuses.
var codexDrop = []string{
	"max_output_tokens", "max_completion_tokens", "max_tokens", "temperature", "top_p", "max_tool_calls",
	"top_logprobs", "truncation", "user", "service_tier", "presence_penalty", "frequency_penalty", "seed",
	"prompt_cache_retention", "safety_identifier", "background",
}

func prepareCodex(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	for _, k := range codexDrop {
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
	if _, ok := m["instructions"]; !ok || m["instructions"] == nil {
		m["instructions"] = ""
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- Copilot -------------------------------------------------------------------

type copilotCreds struct {
	User, Token string
}

func copilotDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	return filepath.Join(userHome(), ".config", "github-copilot")
}

func readCopilot() (copilotCreds, bool) {
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(copilotDir(), name))
		if err != nil {
			continue
		}
		var m map[string]struct {
			User  string `json:"user"`
			Token string `json:"oauth_token"`
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
			if strings.HasPrefix(k, "github.com") && m[k].Token != "" {
				return copilotCreds{User: m[k].User, Token: m[k].Token}, true
			}
		}
	}
	return copilotCreds{}, false
}

func copilotAccount() (Provider, bool) {
	c, ok := readCopilot()
	if !ok {
		return Provider{}, false
	}
	user := c.User
	if user == "" {
		user = "GitHub"
	}
	return Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Catalog: "copilot",
		Account: &Account{Agent: "copilot", User: user}}, true
}

var errCopilotOut = errors.New("copilot: signed out or token rejected — sign in to GitHub Copilot again")

type copilotSession struct {
	token, api string
	expires    time.Time
}

var (
	copilotMu    sync.Mutex
	copilotCache = map[string]copilotSession{}
)

func copilotHeaders(h http.Header) {
	h.Set("Copilot-Integration-Id", copilotIntID)
	h.Set("Editor-Version", copilotEditor)
	h.Set("Editor-Plugin-Version", copilotPlugin)
	h.Set("User-Agent", copilotUA)
	h.Set("Openai-Intent", "conversation-panel")
}

// copilotSessionFor exchanges the GitHub token for a short-lived session
// token, cached until a minute before it expires.
func copilotSessionFor(ctx context.Context) (copilotSession, error) {
	c, ok := readCopilot()
	if !ok {
		return copilotSession{}, errCopilotOut
	}
	copilotMu.Lock()
	defer copilotMu.Unlock()
	key := CopilotTokenURL + "\x00" + c.Token
	if s, ok := copilotCache[key]; ok && time.Until(s.expires) > 60*time.Second {
		return s, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	copilotHeaders(req.Header)
	req.Header.Set("Authorization", "token "+c.Token)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("copilot: token exchange failed: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404 {
		return copilotSession{}, fmt.Errorf("copilot: signed out or no Copilot access (%s) — sign in to GitHub Copilot again", res.Status)
	}
	if res.StatusCode != 200 {
		return copilotSession{}, fmt.Errorf("copilot: token exchange answered %s", res.Status)
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSession{}, errors.New("copilot: token exchange returned no token")
	}
	s := copilotSession{token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/"), expires: time.Unix(v.ExpiresAt, 0)}
	if s.api == "" {
		s.api = copilotDefaultAPI
	}
	copilotCache[key] = s
	return s, nil
}

// lastMessage looks at the final chat message: who sent it, and whether
// any part of it is an image.
func lastMessage(body []byte) (role string, vision bool) {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return "", false
	}
	// vision applies to the whole request
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
	return v.Messages[len(v.Messages)-1].Role, vision
}

// ---- Sign ----------------------------------------------------------------------

// AuthError marks a failure that means "sign in again".
type AuthError struct{ Err error }

func (e *AuthError) Error() string { return e.Err.Error() }
func (e *AuthError) Unwrap() error { return e.Err }

func authErr(err error) error {
	if err == nil {
		return nil
	}
	var ae *AuthError
	if errors.As(err, &ae) {
		return err
	}
	if err == errCodexOut || err == errCopilotOut || strings.Contains(err.Error(), "signed out") {
		return &AuthError{err}
	}
	return err
}

// Sign makes a request to this provider carry its credentials: the API key
// for an ordinary provider, the Agent's own token for an account.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		c, err := codexToken(ctx)
		if err != nil {
			return authErr(err)
		}
		u, err := req.URL.Parse(strings.TrimRight(CodexBase, "/") + "/responses")
		if err != nil {
			return err
		}
		req.URL, req.Host = u, ""
		req.Header.Set("Authorization", "Bearer "+c.Access)
		if c.AccountID != "" {
			req.Header.Set("chatgpt-account-id", c.AccountID)
		}
		req.Header.Set("originator", "codex_cli_rs")
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("Accept", "text/event-stream")
		return nil
	case "copilot":
		s, err := copilotSessionFor(ctx)
		if err != nil {
			return authErr(err)
		}
		u, err := req.URL.Parse(s.api + "/chat/completions")
		if err != nil {
			return err
		}
		req.URL, req.Host = u, ""
		copilotHeaders(req.Header)
		req.Header.Set("Authorization", "Bearer "+s.token)
		role, vision := lastMessage(body)
		if role == "user" {
			req.Header.Set("X-Initiator", "user")
		} else {
			req.Header.Set("X-Initiator", "agent")
		}
		if vision {
			req.Header.Set("Copilot-Vision-Request", "true")
		}
		return nil
	}
	return fmt.Errorf("unknown account agent %q", p.Account.Agent)
}

// ---- accounts and exclusions -------------------------------------------------

// Accounts lists the signed-in agent accounts dial offers as providers.
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

func accountByID(id string) (Provider, bool) {
	switch id {
	case "codex":
		return codexAccount()
	case "copilot":
		return copilotAccount()
	}
	return Provider{}, false
}

var (
	exclMu    sync.Mutex
	exclKey   string
	exclAt    time.Time
	exclKeyOK bool
)

// ResetExcluded forgets the cached keychain probe.
func ResetExcluded() {
	exclMu.Lock()
	exclAt = time.Time{}
	exclMu.Unlock()
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(userHome(), ".claude")
}

// claudeSignedIn only checks that a login exists; it never reads one.
func claudeSignedIn() bool {
	for _, n := range []string{".credentials.json", "credentials.json"} {
		if st, err := os.Stat(filepath.Join(claudeDir(), n)); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			return true
		}
	}
	if runtime.GOOS != "darwin" {
		return false
	}
	// the keychain probe is slow, so its answer is kept for a minute
	key := os.Getenv("CLAUDE_CONFIG_DIR") + "\x00" + userHome()
	exclMu.Lock()
	defer exclMu.Unlock()
	if key == exclKey && time.Since(exclAt) < time.Minute {
		return exclKeyOK
	}
	service := "Claude Code-credentials"
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		sum := sha256.Sum256([]byte(d))
		service += "-" + hex.EncodeToString(sum[:])[:8]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// no -w / -g: attributes only, the secret is never read and no prompt shows
	err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", service).Run()
	exclKey, exclAt, exclKeyOK = key, time.Now(), err == nil
	return exclKeyOK
}

// Excluded lists the sign-ins dial recognises but does not offer.
func Excluded() []Exclusion {
	if claudeSignedIn() {
		return []Exclusion{{Agent: "claude", Why: claudeWhy}}
	}
	return []Exclusion{}
}

// ---- models of accounts --------------------------------------------------------

func accountAvailable(p Provider) []catalog.Model {
	switch p.Account.Agent {
	case "codex":
		return append([]catalog.Model(nil), catalog.Builtin("codex")...)
	case "copilot":
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
	return nil
}

func accountFetch(ctx context.Context, p Provider) ([]catalog.Model, error) {
	if p.Account.Agent == "codex" {
		return accountAvailable(p), nil
	}
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
	copilotHeaders(req.Header)
	req.Header.Set("Authorization", "Bearer "+s.token)
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
	var v struct {
		Data []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled bool   `json:"model_picker_enabled"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					Effort []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("copilot models: not a model list")
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range v.Data {
		if m.ID == "" || seen[m.ID] || m.Capabilities.Type != "chat" || internalModel(m.ID) {
			continue
		}
		if !m.ModelPickerEnabled && m.Policy.State != "enabled" {
			continue
		}
		seen[m.ID] = true
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name, Efforts: m.Capabilities.Supports.Effort})
	}
	if len(out) == 0 {
		return nil, errors.New("copilot: no chat models are available to this account")
	}
	return out, catalog.SaveLive("copilot", s.api, out)
}

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
