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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/dial/internal/catalog"
)

// Account providers are the models an agent's own login already pays for:
// Codex (ChatGPT) and Copilot (GitHub). dial never stores their credentials;
// it reads the agent's own files each time they are needed. Only the user's
// model picks are kept in dial's config.

// Account says whose login backs a provider.
type Account struct {
	Agent string `json:"agent"`          // "Codex", "Copilot"
	User  string `json:"user"`           // email / GitHub login
	Plan  string `json:"plan,omitempty"` // subscription plan, when known
}

// CodexBase is the ChatGPT Codex backend; requests go to CodexBase+"/responses".
var CodexBase = "https://chatgpt.com/backend-api/codex"

// Endpoints dial talks to on behalf of accounts. Tests point them elsewhere.
var (
	CodexRefreshURL = "https://auth.openai.com/oauth/token"
	CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"
)

const (
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	copilotAPIBase = "https://api.githubcopilot.com"
	idCodex        = "codex"
	idCopilot      = "copilot"
	idClaude       = "claude"
)

func isAccountID(id string) bool { return id == idCodex || id == idCopilot }

// IsAccount reports whether the provider is backed by an agent login.
func (p Provider) IsAccount() bool { return p.Account != nil }

func home() string { h, _ := os.UserHomeDir(); return h }

// ---- codex ------------------------------------------------------------------

func codexAuthPath() string { return filepath.Join(home(), ".codex", "auth.json") }

type codexCreds struct {
	Access, Refresh, ID, AccountID string
	Email, Plan                    string
	Expires                        time.Time
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

func readCodex() (*codexCreds, error) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return nil, errors.New("Codex is signed out — run `codex login`")
	}
	var f struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			ID        string `json:"id_token"`
			Access    string `json:"access_token"`
			Refresh   string `json:"refresh_token"`
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil, errors.New("Codex login file is unreadable — run `codex login`")
	}
	if f.AuthMode == "apikey" || (f.AuthMode != "" && f.AuthMode != "chatgpt") || f.Tokens.Access == "" {
		return nil, errors.New("Codex is not signed in with ChatGPT — run `codex login`")
	}
	c := &codexCreds{Access: f.Tokens.Access, Refresh: f.Tokens.Refresh, ID: f.Tokens.ID, AccountID: f.Tokens.AccountID}
	c.fill()
	return c, nil
}

func (c *codexCreds) fill() {
	cl := jwtClaims(c.ID)
	c.Email, c.Plan = "", ""
	if s, ok := cl["email"].(string); ok {
		c.Email = s
	}
	if a, ok := cl["https://api.openai.com/auth"].(map[string]any); ok {
		if s, ok := a["chatgpt_plan_type"].(string); ok {
			c.Plan = s
		}
		if c.AccountID == "" {
			c.AccountID, _ = a["chatgpt_account_id"].(string)
		}
	}
	if c.Email == "" {
		if s, ok := jwtClaims(c.Access)["email"].(string); ok {
			c.Email = s
		}
	}
	c.Expires = time.Time{}
	if e, ok := jwtClaims(c.Access)["exp"].(float64); ok {
		c.Expires = time.Unix(int64(e), 0)
	}
}

// codexFresh returns usable credentials, refreshing them (and writing the
// new tokens back to Codex's own file) when they expire within 5 minutes.
func codexFresh(ctx context.Context) (*codexCreds, error) {
	c, err := readCodex()
	if err != nil {
		return nil, err
	}
	if c.Expires.IsZero() || time.Until(c.Expires) > 5*time.Minute {
		return c, nil
	}
	if c.Refresh == "" {
		return nil, errors.New("Codex login expired — run `codex login`")
	}
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": c.Refresh, "scope": "openid profile email"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CodexRefreshURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Codex token refresh failed: %v — run `codex login` if it persists", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var tr struct {
		ID      string `json:"id_token"`
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &tr) != nil || tr.Access == "" {
		return nil, errors.New("Codex is signed out (token refresh was refused) — run `codex login`")
	}
	if err := writeCodexTokens(tr.ID, tr.Access, tr.Refresh); err != nil {
		return nil, err
	}
	return readCodex()
}

// writeCodexTokens updates the tokens in Codex's file, keeping all else.
func writeCodexTokens(id, access, refresh string) error {
	path := codexAuthPath()
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
	t, _ := m["tokens"].(map[string]any)
	if t == nil {
		t = map[string]any{}
	}
	if id != "" {
		t["id_token"] = id
	}
	t["access_token"] = access
	if refresh != "" {
		t["refresh_token"] = refresh
	}
	m["tokens"] = t
	m["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return os.WriteFile(path, append(out, '\n'), mode)
}

func codexProvider() (Provider, bool) {
	c, err := readCodex()
	if err != nil {
		return Provider{}, false
	}
	user := c.Email
	if user == "" {
		user = "ChatGPT"
	}
	return Provider{ID: idCodex, Name: "Codex", Icon: "codex", Catalog: "openai", Responses: strings.TrimRight(CodexBase, "/"),
		Account: &Account{Agent: "Codex", User: user, Plan: c.Plan}}, true
}

// ---- copilot ----------------------------------------------------------------

func copilotAppsPath() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "github-copilot", "apps.json")
	}
	return filepath.Join(home(), ".config", "github-copilot", "apps.json")
}

func readCopilot() (user, token string, ok bool) {
	b, err := os.ReadFile(copilotAppsPath())
	if err != nil {
		return
	}
	var m map[string]struct {
		User  string `json:"user"`
		Token string `json:"oauth_token"`
	}
	if json.Unmarshal(b, &m) != nil {
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasPrefix(k, "github.com") && m[k].Token != "" {
			return m[k].User, m[k].Token, true
		}
	}
	return
}

func copilotProvider() (Provider, bool) {
	user, _, ok := readCopilot()
	if !ok {
		return Provider{}, false
	}
	return Provider{ID: idCopilot, Name: "Copilot", Icon: "copilot", Catalog: "github-copilot", Chat: copilotAPIBase,
		Account: &Account{Agent: "Copilot", User: user}}, true
}

type copilotSession struct {
	token, api string
	exp        time.Time
}

var (
	sessMu   sync.Mutex
	sessions = map[string]copilotSession{} // by GitHub token; memory only
)

const reloginCopilot = "Copilot session was refused — please sign in to Copilot again"

func copilotSessionFor(ctx context.Context) (copilotSession, error) {
	_, gh, ok := readCopilot()
	if !ok {
		return copilotSession{}, errors.New("Copilot is signed out — please sign in to Copilot again")
	}
	sessMu.Lock()
	s, ok := sessions[gh]
	sessMu.Unlock()
	if ok && time.Until(s.exp) > time.Minute {
		return s, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return s, err
	}
	req.Header.Set("Authorization", "token "+gh)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	req.Header.Set("Editor-Version", "vscode/1.99.3")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.26.7")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return s, fmt.Errorf("Copilot session: %v", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &v) != nil || v.Token == "" {
		return s, errors.New(reloginCopilot)
	}
	s = copilotSession{token: v.Token, api: strings.TrimRight(v.Endpoints.API, "/"), exp: time.Unix(v.ExpiresAt, 0)}
	if s.api == "" {
		s.api = copilotAPIBase
	}
	if v.ExpiresAt == 0 {
		s.exp = time.Now().Add(10 * time.Minute)
	}
	sessMu.Lock()
	sessions[gh] = s
	sessMu.Unlock()
	return s, nil
}

func copilotHeaders(s copilotSession, body []byte) map[string]string {
	h := map[string]string{
		"Authorization":          "Bearer " + s.token,
		"Editor-Version":         "vscode/1.99.3",
		"Editor-Plugin-Version":  "copilot-chat/0.26.7",
		"Copilot-Integration-Id": "vscode-chat",
		"User-Agent":             "GitHubCopilotChat/0.26.7",
		"openai-intent":          "conversation-panel",
		"X-Initiator":            "user",
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) == nil {
		if n := len(req.Messages); n > 0 {
			if r := req.Messages[n-1].Role; r == "tool" || r == "assistant" {
				h["X-Initiator"] = "agent"
			}
		}
		for _, m := range req.Messages {
			if bytes.Contains(m.Content, []byte(`"image_url"`)) {
				h["Copilot-Vision-Request"] = "true"
				break
			}
		}
	}
	return h
}

// copilotModels asks Copilot which chat models this account may use.
func copilotModels(ctx context.Context) ([]catalog.Model, error) {
	s, err := copilotSessionFor(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.api+"/models", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range copilotHeaders(s, nil) {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, errors.New(reloginCopilot)
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("copilot models: %s", res.Status)
	}
	return filterCopilotModels(b)
}

func filterCopilotModels(b []byte) ([]catalog.Model, error) {
	var v struct {
		Data []struct {
			ID                 string `json:"id"`
			Name               string `json:"name"`
			ModelPickerEnabled *bool  `json:"model_picker_enabled"`
			Capabilities       struct {
				Type string `json:"type"`
			} `json:"capabilities"`
			Policy *struct {
				State string `json:"state"`
			} `json:"policy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, errors.New("copilot models: not a model list")
	}
	var out []catalog.Model
	seen := map[string]bool{}
	for _, m := range v.Data {
		id := m.ID
		l := strings.ToLower(id)
		switch {
		case id == "" || seen[id], l == "auto", strings.Contains(l, "embedding"), strings.Contains(l, "internal"), strings.HasPrefix(l, "accounts/"):
			continue
		case m.Capabilities.Type != "chat":
			continue
		case m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled:
			continue
		case m.Policy != nil && m.Policy.State != "" && m.Policy.State != "enabled":
			continue
		}
		seen[id] = true
		name := m.Name
		if name == "" {
			name = id
		}
		out = append(out, catalog.Model{ID: id, Name: name})
	}
	return out, nil
}

// ---- claude -----------------------------------------------------------------

// ClaudeSignedIn reports whether Claude Code has a login. Only the file's
// existence is checked; its content is never read.
func ClaudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(home(), ".claude")
	}
	st, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil && st.Mode().IsRegular()
}

// ClaudeNote explains why a signed-in Claude Code is not offered as a
// provider; ok is false when it is not signed in.
func ClaudeNote() (note string, ok bool) {
	if !ClaudeSignedIn() {
		return "", false
	}
	return "Claude Code 已登录但不提供 (signed in, not offered as a provider): Anthropic's terms do not allow its subscription login to be used by other tools.", true
}

// ---- providers --------------------------------------------------------------

// accounts lists the signed-in account providers, with the user's picks.
func accounts(picks map[string][]string) []Provider {
	var out []Provider
	if p, ok := codexProvider(); ok {
		p.Models = cleanList(picks[idCodex])
		out = append(out, p)
	}
	if p, ok := copilotProvider(); ok {
		p.Models = cleanList(picks[idCopilot])
		out = append(out, p)
	}
	return out
}

var codexDefaults = []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.2"}

var copilotDefaults = []string{"gpt-4.1", "gpt-4o", "gpt-5", "gpt-5-mini", "claude-sonnet-4", "claude-sonnet-4.5", "gemini-2.5-pro", "o4-mini"}

func builtinModels(id string) []catalog.Model {
	ids := codexDefaults
	if id == idCopilot {
		ids = copilotDefaults
	}
	out := make([]catalog.Model, 0, len(ids))
	for _, m := range ids {
		out = append(out, catalog.Model{ID: m, Name: m})
	}
	return out
}

func (p Provider) accountAvailable() []catalog.Model {
	if live, _, ok := catalog.Live(p.ID); ok {
		var out []catalog.Model
		for _, m := range live {
			if p.ID == idCopilot && strings.ToLower(m.ID) == "auto" {
				continue
			}
			out = append(out, m)
		}
		return catalog.Decorate(out, catalog.Provider(p.Catalog))
	}
	return catalog.Decorate(builtinModels(p.ID), catalog.Provider(p.Catalog))
}

func (p Provider) accountFetch(ctx context.Context) ([]catalog.Model, error) {
	if p.ID == idCodex {
		// the Codex backend has no model list; the built-in one stands
		if _, err := codexFresh(ctx); err != nil {
			return nil, err
		}
		return builtinModels(idCodex), nil
	}
	ms, err := copilotModels(ctx)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, errors.New("copilot lists no chat models for this account")
	}
	return ms, catalog.SaveLive(p.ID, p.Chat, ms)
}

// Prepare readies a request to an upstream: the base URL, the headers that
// prove who is asking, and the body adjusted to what the upstream accepts.
// For ordinary providers it changes nothing.
func Prepare(ctx context.Context, p Provider, proto Protocol, body []byte) (base string, headers map[string]string, out []byte, err error) {
	base, out = p.Base(proto), body
	headers = AuthHeaders(p, proto)
	if p.Account == nil {
		return
	}
	switch p.ID {
	case idCodex:
		c, e := codexFresh(ctx)
		if e != nil {
			return "", nil, nil, e
		}
		headers = map[string]string{"Authorization": "Bearer " + c.Access, "chatgpt-account-id": c.AccountID,
			"OpenAI-Beta": "responses=experimental", "originator": "codex_cli_rs"}
		out = codexBody(body)
		base = strings.TrimRight(CodexBase, "/")
	case idCopilot:
		s, e := copilotSessionFor(ctx)
		if e != nil {
			return "", nil, nil, e
		}
		headers = copilotHeaders(s, body)
		base = s.api
	}
	return
}

// codexBody shapes a Responses request the way the ChatGPT backend wants it.
func codexBody(body []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil {
		return body
	}
	if s, ok := m["model"].(string); ok {
		m["model"] = strings.TrimPrefix(s, "codex/")
	}
	m["stream"], m["store"] = true, false
	for _, k := range []string{"max_output_tokens", "temperature", "top_p", "max_tokens", "truncation", "service_tier", "user", "metadata"} {
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
