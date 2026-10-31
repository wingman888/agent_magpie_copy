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

// Accounts are providers that need no key from the user: an agent the user
// already signed in to (Codex with ChatGPT, Copilot with GitHub) keeps its
// own credentials, and dial reads them from there every time it needs them.
// Nothing is copied into dial's own files; sign out of the agent and the
// provider is gone.

// Account says whose subscription an account provider borrows.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
}

// Exclusion is a sign-in dial recognises but does not offer.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// Upstream addresses; variables so tests can point them at a stand-in.
var (
	CodexBase        = "https://chatgpt.com/backend-api/codex"
	CopilotTokenURL  = "https://api.github.com/copilot_internal/v2/token"
	CodexRefreshURL  = "https://auth.openai.com/oauth/token"
	copilotDefaultAP = "https://api.githubcopilot.com"
)

const codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

// SignedOutError means the agent's sign-in is gone or no longer works.
type SignedOutError struct{ Msg string }

func (e *SignedOutError) Error() string { return e.Msg }

var accountIDs = []string{"codex", "copilot"}

func isAccountID(id string) bool { return contains(accountIDs, id) }

// Who is what to show for the provider: the signed-in user, else its name.
func (p Provider) Who() string {
	if p.Account != nil && p.Account.User != "" {
		return p.Account.User
	}
	return p.Name
}

// ---- which accounts exist ---------------------------------------------------

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

func codexAuthPath() string { return filepath.Join(homeDir(), ".codex", "auth.json") }

func copilotDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	return filepath.Join(homeDir(), ".config", "github-copilot")
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(homeDir(), ".claude")
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
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return nil
	}
	return m
}

func jwtExp(tok string) (time.Time, bool) {
	n, ok := jwtClaims(tok)["exp"].(json.Number)
	if !ok {
		return time.Time{}, false
	}
	f, err := n.Float64()
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

type codexAuth struct {
	raw       map[string]any
	access    string
	refresh   string
	idToken   string
	accountID string
}

// readCodexAuth reads Codex's own credential file. ok is false unless it
// holds a ChatGPT sign-in (not an API key) with an access token.
func readCodexAuth() (codexAuth, bool) {
	var a codexAuth
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return a, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&a.raw) != nil || a.raw == nil {
		return a, false
	}
	mode, _ := a.raw["auth_mode"].(string)
	if m := strings.ToLower(mode); strings.Contains(m, "apikey") || strings.Contains(m, "api_key") {
		return a, false
	}
	toks, _ := a.raw["tokens"].(map[string]any)
	str := func(k string) string { s, _ := toks[k].(string); return s }
	a.access, a.refresh, a.idToken, a.accountID = str("access_token"), str("refresh_token"), str("id_token"), str("account_id")
	return a, a.access != ""
}

func codexProvider() (Provider, bool) {
	a, ok := readCodexAuth()
	if !ok {
		return Provider{}, false
	}
	acc := &Account{Agent: "codex", User: "ChatGPT"}
	claims := jwtClaims(a.idToken)
	if e, _ := claims["email"].(string); e != "" {
		acc.User = e
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		acc.Plan, _ = auth["chatgpt_plan_type"].(string)
	}
	return Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Account: acc}, true
}

type copilotLogin struct{ user, token string }

func readCopilot() (copilotLogin, bool) {
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
			if strings.HasPrefix(k, "github.com") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if m[k].Token != "" {
				return copilotLogin{user: m[k].User, token: m[k].Token}, true
			}
		}
	}
	return copilotLogin{}, false
}

func copilotProvider() (Provider, bool) {
	l, ok := readCopilot()
	if !ok {
		return Provider{}, false
	}
	user := l.user
	if user == "" {
		user = "GitHub"
	}
	return Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Account: &Account{Agent: "copilot", User: user}}, true
}

func signedIn(id string) bool {
	switch id {
	case "codex":
		_, ok := readCodexAuth()
		return ok
	case "copilot":
		_, ok := readCopilot()
		return ok
	}
	return false
}

// accountsOf lists the signed-in accounts, minus any id the user has a
// provider of their own for, with the saved model choice applied.
func accountsOf(f file) []Provider {
	own := map[string]bool{}
	picks := map[string][]string{}
	for _, p := range f.Providers {
		if isStub(p) {
			picks[p.ID] = cleanList(p.Models)
		} else {
			own[p.ID] = true
		}
	}
	var out []Provider
	for _, mk := range []func() (Provider, bool){codexProvider, copilotProvider} {
		p, ok := mk()
		if !ok || own[p.ID] {
			continue
		}
		p.Models = picks[p.ID]
		out = append(out, p)
	}
	return out
}

// Accounts lists the signed-in agent accounts dial offers as providers.
func Accounts() []Provider { return accountsOf(load()) }

// isStub is a saved model choice for an account provider: an id of one and
// no address of its own.
func isStub(p Provider) bool {
	return isAccountID(strings.ToLower(strings.TrimSpace(p.ID))) && p.Chat == "" && p.Responses == "" && p.Anthropic == ""
}

// ---- sign-ins dial recognises but does not offer ------------------------------

const claudeWhy = "Anthropic's terms only allow a Claude subscription to be used with Claude Code itself, so dial cannot lend it to other agents. Add an Anthropic API key as a provider instead (dial provider add anthropic <key>)."

var (
	exclMu    sync.Mutex
	exclKey   string
	exclAt    time.Time
	exclValue []Exclusion
)

// Excluded lists the sign-ins dial found but does not offer, and why. The
// answer is kept for about a minute.
func Excluded() []Exclusion {
	key := claudeDir() + "|" + runtime.GOOS
	exclMu.Lock()
	defer exclMu.Unlock()
	if exclKey == key && time.Since(exclAt) < time.Minute {
		return append([]Exclusion(nil), exclValue...)
	}
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	exclKey, exclAt, exclValue = key, time.Now(), out
	return append([]Exclusion(nil), out...)
}

// claudeSignedIn only asks whether a sign-in exists; it never reads one.
func claudeSignedIn() bool {
	if st, err := os.Stat(filepath.Join(claudeDir(), ".credentials.json")); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
		return true
	}
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// no -w / -g: this prints attributes only, never the secret, and
		// never raises the keychain prompt
		cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials")
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		return cmd.Run() == nil
	}
	return false
}

// ---- signing ---------------------------------------------------------------

// Sign makes a request to this provider prove who it is: the API key for an
// ordinary provider, the agent's own token for an account.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case "codex":
		return signCodex(ctx, req, proto)
	case "copilot":
		return signCopilot(ctx, req, proto, body)
	}
	return fmt.Errorf("unknown account %q", p.Account.Agent)
}

// Prepare adjusts a request body to what the backend insists on.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" {
		return prepareCodex(body)
	}
	return body
}

func setURL(req *http.Request, raw string) error {
	u, err := req.URL.Parse(raw)
	if err != nil {
		return err
	}
	req.URL, req.Host = u, ""
	return nil
}

// ---- Codex -----------------------------------------------------------------

var codexMu sync.Mutex

func codexSignedOut(why string) error {
	m := "Codex signed out, please run `codex login` again"
	if why != "" {
		m += " (" + why + ")"
	}
	return &SignedOutError{Msg: m}
}

func expiring(tok string) bool {
	exp, ok := jwtExp(tok)
	return ok && time.Until(exp) < 5*time.Minute
}

func signCodex(ctx context.Context, req *http.Request, proto Protocol) error {
	if proto != Responses {
		return fmt.Errorf("Codex speaks only the Responses API, not %s", proto)
	}
	a, ok := readCodexAuth()
	if !ok {
		return codexSignedOut("")
	}
	if expiring(a.access) {
		var err error
		if a, err = refreshCodex(ctx); err != nil {
			return err
		}
	}
	req.Header.Set("Authorization", "Bearer "+a.access)
	if a.accountID != "" {
		req.Header.Set("chatgpt-account-id", a.accountID)
	}
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	return setURL(req, strings.TrimRight(CodexBase, "/")+"/responses")
}

// refreshCodex trades the refresh token for new tokens and writes them back
// into Codex's file, every other field kept, so Codex itself keeps working.
func refreshCodex(ctx context.Context) (codexAuth, error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	a, ok := readCodexAuth() // another request may have refreshed while we waited
	if !ok {
		return a, codexSignedOut("")
	}
	if !expiring(a.access) {
		return a, nil
	}
	if a.refresh == "" {
		return a, codexSignedOut("no refresh token")
	}
	payload, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": a.refresh, "scope": "openid profile email"})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(payload))
	if err != nil {
		return a, codexSignedOut(err.Error())
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		return a, codexSignedOut("token refresh failed: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return a, codexSignedOut("token refresh failed: " + res.Status)
	}
	var out struct {
		Access  string `json:"access_token"`
		ID      string `json:"id_token"`
		Refresh string `json:"refresh_token"`
	}
	if json.Unmarshal(b, &out) != nil || out.Access == "" {
		return a, codexSignedOut("token refresh returned no access token")
	}
	toks, _ := a.raw["tokens"].(map[string]any)
	if toks == nil {
		toks = map[string]any{}
		a.raw["tokens"] = toks
	}
	toks["access_token"] = out.Access
	a.access = out.Access
	if out.ID != "" {
		toks["id_token"], a.idToken = out.ID, out.ID
	}
	if out.Refresh != "" {
		toks["refresh_token"], a.refresh = out.Refresh, out.Refresh
	}
	a.raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeCodexAuth(a.raw); err != nil {
		return a, fmt.Errorf("could not save the refreshed Codex token: %w", err)
	}
	return a, nil
}

func writeCodexAuth(raw map[string]any) error {
	path := codexAuthPath()
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".auth-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// prepareCodex shapes a Responses request the way the ChatGPT backend wants
// it: streamed, not stored, input as a list, none of the limits it rejects.
func prepareCodex(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	m["stream"], m["store"] = true, false
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}}}}
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	for _, k := range []string{"max_output_tokens", "max_tokens", "max_completion_tokens", "temperature", "top_p",
		"presence_penalty", "frequency_penalty", "stream_options", "truncation", "service_tier", "user", "metadata"} {
		delete(m, k)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// ---- Copilot ---------------------------------------------------------------

const (
	copilotEditor  = "vscode/1.99.3"
	copilotPlugin  = "copilot-chat/0.26.7"
	copilotAgent   = "GitHubCopilotChat/0.26.7"
	copilotService = "vscode-chat"
)

type copilotSession struct {
	token, api string
	exp        time.Time
}

var (
	copMu       sync.Mutex
	copSessions = map[string]copilotSession{}
	copModels   = map[string][]catalog.Model{}
)

func copilotSignedOut(why string) error {
	m := "Copilot signed out, please sign in to GitHub Copilot again"
	if why != "" {
		m += " (" + why + ")"
	}
	return &SignedOutError{Msg: m}
}

// copilotSessionFor returns a short-lived session token for the signed-in
// GitHub account, exchanging again when it is about to run out.
func copilotSessionFor(ctx context.Context) (copilotSession, error) {
	l, ok := readCopilot()
	if !ok {
		return copilotSession{}, copilotSignedOut("")
	}
	copMu.Lock()
	defer copMu.Unlock()
	if s, ok := copSessions[l.token]; ok && time.Until(s.exp) > 60*time.Second {
		return s, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	r.Header.Set("Authorization", "token "+l.token)
	r.Header.Set("Accept", "application/json")
	r.Header.Set("Editor-Version", copilotEditor)
	r.Header.Set("Editor-Plugin-Version", copilotPlugin)
	r.Header.Set("User-Agent", copilotAgent)
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		return copilotSession{}, fmt.Errorf("Copilot token exchange failed: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	switch {
	case res.StatusCode == 401 || res.StatusCode == 403 || res.StatusCode == 404:
		delete(copSessions, l.token)
		return copilotSession{}, copilotSignedOut("GitHub said " + res.Status)
	case res.StatusCode < 200 || res.StatusCode >= 300:
		return copilotSession{}, fmt.Errorf("Copilot token exchange failed: %s", APIError(b, res.Status))
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if json.Unmarshal(b, &v) != nil || v.Token == "" {
		return copilotSession{}, errors.New("Copilot token exchange returned no token")
	}
	s := copilotSession{token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/"), exp: time.Unix(v.ExpiresAt, 0)}
	if s.api == "" {
		s.api = copilotDefaultAP
	}
	if v.ExpiresAt == 0 {
		s.exp = time.Now().Add(20 * time.Minute)
	}
	copSessions[l.token] = s
	return s, nil
}

func copilotHeaders(h http.Header, token string) {
	h.Set("Authorization", "Bearer "+token)
	h.Set("Copilot-Integration-Id", copilotService)
	h.Set("Editor-Version", copilotEditor)
	h.Set("Editor-Plugin-Version", copilotPlugin)
	h.Set("User-Agent", copilotAgent)
	h.Set("Openai-Intent", "conversation-panel")
}

func signCopilot(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if proto != Chat {
		return fmt.Errorf("Copilot is reached through Chat Completions, not %s", proto)
	}
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return err
	}
	copilotHeaders(req.Header, s.token)
	initiator, vision := copilotShape(body)
	req.Header.Set("X-Initiator", initiator)
	if vision {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	return setURL(req, s.api+"/chat/completions")
}

// copilotShape tells who started the turn (the user, or the agent working
// through tool calls) and whether any message carries an image.
func copilotShape(body []byte) (initiator string, vision bool) {
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
			Type     string          `json:"type"`
			ImageURL json.RawMessage `json:"image_url"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" || len(p.ImageURL) > 0 {
				vision = true
			}
		}
	}
	return
}

// fetchCopilot asks Copilot which chat models this account may use.
func fetchCopilot(ctx context.Context) ([]catalog.Model, error) {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/json")
	copilotHeaders(r.Header, s.token)
	res, err := http.DefaultClient.Do(r)
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
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled bool   `json:"model_picker_enabled"`
			Policy             struct {
				State string `json:"state"`
			} `json:"policy"`
			Capabilities struct {
				Type     string `json:"type"`
				Supports struct {
					Reasoning []string `json:"reasoning_effort"`
				} `json:"supports"`
			} `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("Copilot models: not a model list")
	}
	var out []catalog.Model
	for _, m := range v.Data {
		if m.Capabilities.Type != "chat" || internalCopilot(m.ID) {
			continue
		}
		if !m.ModelPickerEnabled && m.Policy.State != "enabled" {
			continue
		}
		name := m.Name
		if name == "" {
			name = m.ID
		}
		out = append(out, catalog.Model{ID: m.ID, Name: name, Efforts: m.Capabilities.Supports.Reasoning})
	}
	if len(out) == 0 {
		return nil, errors.New("Copilot lists no chat models for this account")
	}
	if l, ok := readCopilot(); ok {
		copMu.Lock()
		copModels[l.token] = out
		copMu.Unlock()
	}
	return out, nil
}

func internalCopilot(id string) bool {
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

// accountModels is what an account is known to serve, before any choice.
func (p Provider) accountModels() []catalog.Model {
	switch p.Account.Agent {
	case "codex":
		return catalog.Builtin("codex")
	case "copilot":
		if l, ok := readCopilot(); ok {
			copMu.Lock()
			ms, ok := copModels[l.token]
			copMu.Unlock()
			if ok {
				return ms
			}
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
