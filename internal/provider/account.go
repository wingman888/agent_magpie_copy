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

// Accounts are providers that need no key from the user: an agent on this
// machine is already signed in, and dial borrows that sign-in. The agent's
// own credential file stays the only copy — dial reads it each time it is
// needed and never stores a token. What dial keeps for an account provider
// is the list of models the user chose to expose, nothing else.

// Account says whose sign-in a provider borrows. It never holds a token.
type Account struct {
	Agent string `json:"agent"` // codex, copilot
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
}

// Exclusion is a sign-in dial recognises but does not offer.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// ErrSignedOut means the agent's sign-in is gone or was refused.
var ErrSignedOut = errors.New("signed out")

// Endpoints, variable so tests can point them at a stand-in.
var (
	CodexBase       = "https://chatgpt.com/backend-api/codex"
	CodexRefreshURL = "https://auth.openai.com/oauth/token"
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	copilotAPIBase = "https://api.githubcopilot.com"
)

// AgentName is how an agent is called in messages.
func AgentName(agent string) string {
	switch agent {
	case "codex":
		return "Codex"
	case "copilot":
		return "Copilot"
	case "claude":
		return "Claude Code"
	}
	return agent
}

func isAccountID(id string) bool { return id == "codex" || id == "copilot" }

// ---- which accounts are offered ---------------------------------------------

// Accounts lists the signed-in agents dial offers as providers. A provider
// the user made by hand under the same id wins over the account.
func Accounts() []Provider {
	stored := load().Providers
	custom := map[string]bool{}
	picks := map[string][]string{}
	for _, p := range stored {
		id := strings.ToLower(strings.TrimSpace(p.ID))
		if !isAccountID(id) {
			continue
		}
		if hasBase(p) {
			custom[id] = true
		} else {
			picks[id] = cleanList(p.Models)
		}
	}
	var out []Provider
	if c, ok := readCodex(); ok && !custom["codex"] {
		out = append(out, Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Responses: CodexBase,
			Models: picks["codex"], Website: "https://chatgpt.com/codex",
			Account: &Account{Agent: "codex", User: c.user, Plan: c.plan}})
	}
	if c, ok := readCopilot(); ok && !custom["copilot"] {
		out = append(out, Provider{ID: "copilot", Name: "Copilot", Icon: "githubcopilot", Chat: copilotAPIBase,
			Models: picks["copilot"], Website: "https://github.com/features/copilot",
			Account: &Account{Agent: "copilot", User: c.user}})
	}
	return out
}

func hasBase(p Provider) bool {
	return strings.TrimSpace(p.Chat) != "" || strings.TrimSpace(p.Responses) != "" || strings.TrimSpace(p.Anthropic) != ""
}

// claudeWhy is why a Claude Code sign-in is not offered. It names no credential.
const claudeWhy = "Anthropic's terms only allow a Claude subscription to be used with Claude Code itself, " +
	"so dial does not lend it to other agents. To use Claude models through dial, add an Anthropic API key as a provider " +
	"(dial provider add anthropic <key>)."

var (
	exclMu   sync.Mutex
	exclAt   time.Time
	exclKey  string
	exclKeyc bool
)

// Excluded lists the sign-ins dial found but will not offer, and why.
// The macOS keychain lookup is remembered for about a minute.
func Excluded() []Exclusion {
	if claudeSignedIn() {
		return []Exclusion{{Agent: "claude", Why: claudeWhy}}
	}
	return []Exclusion{}
}

func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// claudeSignedIn only asks whether a sign-in exists; it never reads one.
func claudeSignedIn() bool {
	dir := claudeDir()
	for _, name := range []string{".credentials.json", "credentials.json"} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() && st.Size() > 0 {
			return true
		}
	}
	if runtime.GOOS != "darwin" {
		return false
	}
	exclMu.Lock()
	defer exclMu.Unlock()
	key := dir
	if exclKey == key && time.Since(exclAt) < time.Minute {
		return exclKeyc
	}
	// no -w / -g: that only reports whether the item exists, with no prompt
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	found := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials").Run() == nil
	exclKey, exclAt, exclKeyc = key, time.Now(), found
	return found
}

// ---- storing the model choice ------------------------------------------------

// accountRecord reports whether a stored row is only a model choice for an
// account provider.
func accountRecord(p Provider) bool {
	return isAccountID(strings.ToLower(strings.TrimSpace(p.ID))) && !hasBase(p)
}

// saveAccount keeps the model choice of an account provider, and nothing else.
func saveAccount(id string, models []string) error {
	f := load()
	keep := f.Providers[:0]
	for _, p := range f.Providers {
		if strings.ToLower(strings.TrimSpace(p.ID)) == id && accountRecord(p) {
			continue
		}
		keep = append(keep, p)
	}
	f.Providers = keep
	if models = cleanList(models); len(models) > 0 {
		f.Providers = append(f.Providers, Provider{ID: id, Models: models})
	}
	return store(f)
}

// ---- signing --------------------------------------------------------------------

// Sign makes a request to the provider carry the right credentials: the key
// for ordinary providers, the signed-in agent's token for accounts. body is
// the request body, which some backends want a hint from.
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
	return fmt.Errorf("%s: unknown account", p.Name)
}

// Prepare reshapes a request body to what the backend accepts.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" {
		return prepareCodex(body)
	}
	return body
}

// AuthHint is a sentence to add to a failure from an account's backend.
func (p Provider) AuthHint() string {
	if p.Account == nil {
		return ""
	}
	return "sign in to " + AgentName(p.Account.Agent) + " again"
}

func signedOut(agent, why string) error {
	return fmt.Errorf("%s is signed out (%s) — sign in to %s again: %w", AgentName(agent), why, AgentName(agent), ErrSignedOut)
}

// ---- Codex ------------------------------------------------------------------------

type codexCreds struct {
	user, plan, access, accountID string
	exp                           time.Time
}

func codexPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

type codexFile struct {
	Mode   string `json:"auth_mode"`
	Tokens struct {
		ID      string `json:"id_token"`
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Account string `json:"account_id"`
	} `json:"tokens"`
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

func readCodexFile() (codexFile, bool) {
	var f codexFile
	b, err := os.ReadFile(codexPath())
	if err != nil || json.Unmarshal(b, &f) != nil {
		return f, false
	}
	mode := strings.ToLower(strings.ReplaceAll(f.Mode, "_", ""))
	if mode == "apikey" || f.Tokens.Access == "" {
		return f, false
	}
	return f, true
}

func codexInfo(f codexFile) codexCreds {
	c := codexCreds{user: "ChatGPT", access: f.Tokens.Access, accountID: f.Tokens.Account}
	if cl := jwtClaims(f.Tokens.ID); cl != nil {
		if e, _ := cl["email"].(string); e != "" {
			c.user = e
		}
		if a, ok := cl["https://api.openai.com/auth"].(map[string]any); ok {
			c.plan, _ = a["chatgpt_plan_type"].(string)
			if c.accountID == "" {
				c.accountID, _ = a["chatgpt_account_id"].(string)
			}
		}
	}
	if cl := jwtClaims(f.Tokens.Access); cl != nil {
		if e, ok := cl["exp"].(float64); ok {
			c.exp = time.Unix(int64(e), 0)
		}
	}
	return c
}

func readCodex() (codexCreds, bool) {
	f, ok := readCodexFile()
	if !ok {
		return codexCreds{}, false
	}
	return codexInfo(f), true
}

var codexMu sync.Mutex

// codexToken returns a usable access token, refreshing it first when it
// expires within five minutes. Codex's own file stays the one copy.
func codexToken(ctx context.Context) (codexCreds, error) {
	f, ok := readCodexFile()
	if !ok {
		return codexCreds{}, signedOut("codex", "no ChatGPT sign-in found")
	}
	c := codexInfo(f)
	if c.exp.IsZero() || time.Until(c.exp) > 5*time.Minute {
		return c, nil
	}
	codexMu.Lock()
	defer codexMu.Unlock()
	f, ok = readCodexFile() // someone may have refreshed while we waited
	if !ok {
		return codexCreds{}, signedOut("codex", "no ChatGPT sign-in found")
	}
	c = codexInfo(f)
	if c.exp.IsZero() || time.Until(c.exp) > 5*time.Minute {
		return c, nil
	}
	if f.Tokens.Refresh == "" {
		return codexCreds{}, signedOut("codex", "the session cannot be refreshed")
	}
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": f.Tokens.Refresh, "scope": "openid profile email"})
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return codexCreds{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return codexCreds{}, signedOut("codex", "refreshing the token failed: "+err.Error())
	}
	defer res.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var nt struct {
		Access  string `json:"access_token"`
		ID      string `json:"id_token"`
		Refresh string `json:"refresh_token"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(rb, &nt) != nil || nt.Access == "" {
		return codexCreds{}, signedOut("codex", fmt.Sprintf("refreshing the token failed: %s", res.Status))
	}
	if err := writeCodexTokens(nt.Access, nt.ID, nt.Refresh); err != nil {
		return codexCreds{}, fmt.Errorf("Codex token refreshed but auth.json could not be updated: %w", err)
	}
	f, ok = readCodexFile()
	if !ok {
		return codexCreds{}, signedOut("codex", "no ChatGPT sign-in found")
	}
	return codexInfo(f), nil
}

// writeCodexTokens puts refreshed tokens back into Codex's auth.json,
// keeping every other field and the private file mode.
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
	toks, _ := m["tokens"].(map[string]any)
	if toks == nil {
		toks = map[string]any{}
	}
	toks["access_token"] = access
	if id != "" {
		toks["id_token"] = id
	}
	if refresh != "" {
		toks["refresh_token"] = refresh
	}
	m["tokens"] = toks
	m["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(m, "", "  ")
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
	if _, err := tmp.Write(append(out, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func signCodex(ctx context.Context, req *http.Request) error {
	c, err := codexToken(ctx)
	if err != nil {
		return err
	}
	if u, err := url.Parse(strings.TrimRight(CodexBase, "/") + "/responses"); err == nil {
		req.URL = u
		req.Host = ""
	}
	req.Header.Set("Authorization", "Bearer "+c.access)
	if c.accountID != "" {
		req.Header.Set("chatgpt-account-id", c.accountID)
	}
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	return nil
}

// codexDrop are the sampling and limit fields the ChatGPT backend refuses.
var codexDrop = []string{"max_output_tokens", "max_tokens", "max_completion_tokens", "temperature", "top_p",
	"truncation", "presence_penalty", "frequency_penalty", "seed", "stop", "n", "logit_bias", "user"}

func prepareCodex(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return body
	}
	m["stream"] = true
	m["store"] = false
	for _, k := range codexDrop {
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

// ---- Copilot ------------------------------------------------------------------------

type copilotCreds struct{ user, token string }

func copilotDir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "github-copilot")
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
				u := m[k].User
				if u == "" {
					u = "GitHub"
				}
				return copilotCreds{user: u, token: m[k].Token}, true
			}
		}
	}
	return copilotCreds{}, false
}

type copilotSession struct {
	token, api string
	exp        time.Time
}

var (
	copilotMu    sync.Mutex
	copilotCache = map[string]copilotSession{}
)

// copilotSessionFor trades the account's GitHub token for a short-lived
// session token, kept until shortly before it expires.
func copilotSessionFor(ctx context.Context) (copilotSession, error) {
	c, ok := readCopilot()
	if !ok {
		return copilotSession{}, signedOut("copilot", "no GitHub Copilot sign-in found")
	}
	copilotMu.Lock()
	defer copilotMu.Unlock()
	key := CopilotTokenURL + "\n" + c.token
	if s, ok := copilotCache[key]; ok && time.Until(s.exp) > 2*time.Minute {
		return s, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSession{}, err
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	copilotHeaders(req)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSession{}, fmt.Errorf("Copilot token exchange failed: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound {
		return copilotSession{}, signedOut("copilot", "GitHub refused the sign-in: "+res.Status)
	}
	if res.StatusCode != http.StatusOK {
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
		return copilotSession{}, errors.New("Copilot token exchange failed: unexpected reply")
	}
	s := copilotSession{token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/"), exp: time.Unix(v.ExpiresAt, 0)}
	if s.api == "" {
		s.api = copilotAPIBase
	}
	copilotCache[key] = s
	return s, nil
}

func copilotHeaders(req *http.Request) {
	req.Header.Set("Copilot-Integration-Id", "vscode-chat")
	req.Header.Set("Editor-Version", "vscode/1.99.3")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
}

func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(s.api + "/chat/completions")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = ""
	copilotHeaders(req)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("OpenAI-Intent", "conversation-edits")
	initiator, vision := copilotShape(body)
	req.Header.Set("X-Initiator", initiator)
	if vision {
		req.Header.Set("Copilot-Vision-Request", "true")
	}
	return nil
}

// copilotShape tells who started this turn (the user, or the agent working
// through tool results) and whether the request carries images.
func copilotShape(body []byte) (initiator string, vision bool) {
	initiator = "user"
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil || len(v.Messages) == 0 {
		return
	}
	if v.Messages[len(v.Messages)-1].Role != "user" {
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
	return
}

// internal model ids Copilot lists but are not for chatting.
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

// fetchCopilot asks Copilot which chat models this account can use.
func fetchCopilot(ctx context.Context) ([]catalog.Model, string, error) {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return nil, "", err
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, "", err
	}
	copilotHeaders(req)
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Copilot model list: %s", APIError(b, res.Status))
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
		return nil, "", errors.New("Copilot model list: not a model list")
	}
	var out []catalog.Model
	for _, m := range v.Data {
		if m.ID == "" || m.Capabilities.Type != "chat" || copilotInternal(m.ID) {
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
		return nil, "", errors.New("Copilot lists no chat model this account can use")
	}
	return out, s.api, nil
}

// builtinAccountModels is the list used before any refresh.
func builtinAccountModels(agent string) []catalog.Model {
	var out []catalog.Model
	switch agent {
	case "codex":
		out = append(out, catalog.Builtin("codex")...)
	case "copilot":
		for _, m := range catalog.Builtin("copilot") {
			if m.ID != "auto" {
				out = append(out, m)
			}
		}
	}
	return out
}
