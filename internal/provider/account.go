package provider

// Account providers borrow a sign-in that another agent already keeps: the
// Codex CLI's ChatGPT login and the GitHub Copilot login. dial never stores
// their credentials. Each use reads the agent's own file, so a logout in the
// agent removes the provider here at once, and a refreshed token is picked up.

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
)

// Account says whose sign-in a provider borrows.
type Account struct {
	Agent string `json:"agent"`          // "codex" or "copilot"
	User  string `json:"user"`           // ChatGPT email, or GitHub user name
	Plan  string `json:"plan,omitempty"` // subscription tier, when the sign-in says
}

// Exclusion is a sign-in dial recognises but does not offer as a provider.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

const (
	codexID   = "codex"
	copilotID = "copilot"
)

// CodexBase is the ChatGPT backend Codex talks to. A variable so tests can
// point it at a stand-in server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CopilotTokenURL is where a GitHub OAuth token is exchanged for a short-lived
// Copilot session token. A variable so tests can point it at a stand-in.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

const claudeWhy = "Anthropic's terms allow a Claude subscription to be used only by Claude Code itself, " +
	"so dial does not offer it as a provider. Add an Anthropic API key as a provider instead."

// copilotEditor identifies dial's requests to Copilot the way an editor would.
const (
	copilotIntegration = "vscode-chat"
	copilotUA          = "GitHubCopilotChat/0.26.0"
	copilotEditor      = "vscode/1.99.0"
	copilotPlugin      = "copilot-chat/0.26.0"
)

// isAccountID reports whether an id is reserved for an account provider.
func isAccountID(id string) bool { return id == codexID || id == copilotID }

// hasAddress is true for a provider the user built, which carries a base URL.
func hasAddress(p Provider) bool { return p.Chat != "" || p.Responses != "" || p.Anthropic != "" }

// ---- reading the sign-ins --------------------------------------------------

type codexLogin struct {
	token, accountID, user, plan string
}

// readCodex reads ~/.codex/auth.json. It is false in API-key mode, when the
// file is missing, or when it carries no ChatGPT access token.
func readCodex() (codexLogin, bool) {
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		return codexLogin{}, false
	}
	var f struct {
		AuthMode string `json:"auth_mode"`
		Tokens   struct {
			IDToken     string `json:"id_token"`
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(b, &f) != nil {
		return codexLogin{}, false
	}
	if strings.EqualFold(f.AuthMode, "apikey") || strings.EqualFold(f.AuthMode, "api_key") {
		return codexLogin{}, false
	}
	if f.Tokens.AccessToken == "" {
		return codexLogin{}, false
	}
	l := codexLogin{token: f.Tokens.AccessToken, accountID: f.Tokens.AccountID, user: "ChatGPT"}
	claims := jwtClaims(f.Tokens.IDToken)
	if email, _ := claims["email"].(string); email != "" {
		l.user = email
	}
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if plan, _ := auth["chatgpt_plan_type"].(string); plan != "" {
			l.plan = plan
		}
		if id, _ := auth["chatgpt_account_id"].(string); l.accountID == "" && id != "" {
			l.accountID = id
		}
	}
	return l, true
}

// jwtClaims decodes the payload segment of a JWT. Nil when it is not one.
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

type copilotLogin struct {
	oauth, user string
}

// readCopilot reads the GitHub OAuth entry the editor plugins keep under
// $XDG_CONFIG_HOME/github-copilot (apps.json first, then hosts.json).
func readCopilot() (copilotLogin, bool) {
	dir := filepath.Join(xdgConfigHome(), "github-copilot")
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var entries map[string]json.RawMessage
		if json.Unmarshal(b, &entries) != nil {
			continue
		}
		keys := make([]string, 0, len(entries))
		for k := range entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if !strings.HasPrefix(k, "github.com") {
				continue
			}
			var e struct {
				User  string `json:"user"`
				OAuth string `json:"oauth_token"`
			}
			if json.Unmarshal(entries[k], &e) != nil || e.OAuth == "" {
				continue
			}
			user := e.User
			if user == "" {
				user = "GitHub"
			}
			return copilotLogin{oauth: e.OAuth, user: user}, true
		}
	}
	return copilotLogin{}, false
}

func xdgConfigHome() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// claudeSignedIn only asks whether a Claude Code login exists. The file is
// never opened; the macOS keychain is asked by exit code alone.
func claudeSignedIn() bool {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	if st, err := os.Stat(filepath.Join(dir, ".credentials.json")); err == nil && !st.IsDir() {
		return true
	}
	return claudeKeychain()
}

var keychain struct {
	sync.Mutex
	at time.Time
	ok bool
}

// claudeKeychain runs `security find-generic-password` with no -w flag, so the
// secret is never read. Its answer is remembered for a minute.
func claudeKeychain() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	keychain.Lock()
	defer keychain.Unlock()
	if time.Since(keychain.at) < time.Minute {
		return keychain.ok
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials")
	keychain.ok = cmd.Run() == nil
	keychain.at = time.Now()
	return keychain.ok
}

// ---- the account providers -------------------------------------------------

// Excluded lists the sign-ins dial recognises but does not offer.
func Excluded() []Exclusion {
	var out []Exclusion
	if claudeSignedIn() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	return out
}

// Accounts lists the account providers whose sign-in is present and that
// the user has not shadowed with a provider of their own.
func Accounts() []Provider {
	f := load()
	return accountsOf(f, userBuilt(f))
}

// userBuilt is the set of ids the user gave to providers with a base URL.
func userBuilt(f file) map[string]bool {
	set := map[string]bool{}
	for _, p := range f.Providers {
		if hasAddress(p) {
			set[p.ID] = true
		}
	}
	return set
}

// accountsOf builds the signed-in account providers from the stored
// selections in f, leaving out ids in shadow.
func accountsOf(f file, shadow map[string]bool) []Provider {
	var out []Provider
	for _, p := range accountProviders(f) {
		if !shadow[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// accountProviders reads each sign-in and attaches the model selection stored
// for it, if any.
func accountProviders(f file) []Provider {
	var out []Provider
	if l, ok := readCodex(); ok {
		out = append(out, withSelection(f, Provider{
			ID: codexID, Name: "Codex", Icon: "openai", Catalog: "openai", Website: "https://chatgpt.com",
			Account: &Account{Agent: "codex", User: l.user, Plan: l.plan},
		}))
	}
	if l, ok := readCopilot(); ok {
		out = append(out, withSelection(f, Provider{
			ID: copilotID, Name: "Copilot", Website: "https://github.com/features/copilot",
			Account: &Account{Agent: "copilot", User: l.user},
		}))
	}
	return out
}

// withSelection copies the stored model selection for an account onto p.
func withSelection(f file, p Provider) Provider {
	for _, s := range f.Providers {
		if s.ID == p.ID && !hasAddress(s) {
			p.Models = cleanList(s.Models)
			break
		}
	}
	return p
}

// accountSignedIn reports whether the agent behind an account id is signed in.
func accountSignedIn(id string) bool {
	switch id {
	case codexID:
		_, ok := readCodex()
		return ok
	case copilotID:
		_, ok := readCopilot()
		return ok
	}
	return false
}

// signedOutErr tells the user to sign in again, without naming any secret.
func signedOutErr(id string) error {
	if id == codexID {
		return fmt.Errorf("codex: signed out; run `codex login` to sign in with ChatGPT again")
	}
	return fmt.Errorf("copilot: signed out; sign in to GitHub Copilot in your editor again")
}

// Sign sets the headers that let a request reach this provider. A provider
// with a key gets it as usual; an account provider gets the agent's token.
func (p Provider) Sign(ctx context.Context, req *http.Request, proto Protocol, body []byte) error {
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if p.Account == nil {
		for k, v := range AuthHeaders(p, proto) {
			req.Header.Set(k, v)
		}
		return nil
	}
	switch p.Account.Agent {
	case codexID:
		return signCodex(req)
	case copilotID:
		return signCopilot(ctx, req, body)
	}
	return fmt.Errorf("%s: unknown account type %q", p.ID, p.Account.Agent)
}

func signCodex(req *http.Request) error {
	l, ok := readCodex()
	if !ok {
		return signedOutErr(codexID)
	}
	u, err := url.Parse(CodexBase + "/responses")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+l.token)
	if l.accountID != "" {
		req.Header.Set("chatgpt-account-id", l.accountID)
	}
	return nil
}

func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	l, ok := readCopilot()
	if !ok {
		return signedOutErr(copilotID)
	}
	tok, api, err := copilotSession(ctx, l.oauth)
	if err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimRight(api, "/") + "/chat/completions")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+tok)
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

// chatMessages reads the messages of a Chat Completions body.
type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func chatMessages(body []byte) []chatMessage {
	var v struct {
		Messages []chatMessage `json:"messages"`
	}
	if json.Unmarshal(body, &v) != nil {
		return nil
	}
	return v.Messages
}

// initiator is "user" when the last message is the user's, else "agent".
func initiator(body []byte) string {
	ms := chatMessages(body)
	if len(ms) == 0 || ms[len(ms)-1].Role == "user" {
		return "user"
	}
	return "agent"
}

// hasImage is true when any message carries an image part.
func hasImage(body []byte) bool {
	for _, m := range chatMessages(body) {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			switch p.Type {
			case "image_url", "input_image", "image":
				return true
			}
		}
	}
	return false
}

type copilotSessionTok struct {
	token, api string
	exp        time.Time
}

var copilotCache struct {
	sync.Mutex
	m map[string]copilotSessionTok
}

// copilotRefreshAhead is how close to expiry a session token is replaced.
const copilotRefreshAhead = 5 * time.Minute

// copilotSession returns a Copilot session token and its API base for an
// OAuth token, exchanging it again only when the cached one nears expiry.
func copilotSession(ctx context.Context, oauth string) (string, string, error) {
	key := CopilotTokenURL + "\x00" + oauth
	copilotCache.Lock()
	defer copilotCache.Unlock()
	if t, ok := copilotCache.m[key]; ok && time.Until(t.exp) > copilotRefreshAhead {
		return t.token, t.api, nil
	}
	t, err := exchangeCopilot(ctx, oauth)
	if err != nil {
		return "", "", err
	}
	if copilotCache.m == nil {
		copilotCache.m = map[string]copilotSessionTok{}
	}
	copilotCache.m[key] = t
	return t.token, t.api, nil
}

func exchangeCopilot(ctx context.Context, oauth string) (copilotSessionTok, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSessionTok{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUA)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return copilotSessionTok{}, fmt.Errorf("copilot: token exchange failed: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return copilotSessionTok{}, fmt.Errorf("copilot: GitHub refused the sign-in (%s); sign in to GitHub Copilot again",
			APIError(b, res.Status))
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Token == "" || v.Endpoints.API == "" {
		return copilotSessionTok{}, errors.New("copilot: unexpected token exchange response")
	}
	return copilotSessionTok{token: v.Token, api: v.Endpoints.API, exp: time.Unix(v.ExpiresAt, 0)}, nil
}

// Prepare adjusts a request body to what the provider's backend accepts.
// Codex's ChatGPT backend wants a streamed, non-stored request with its input
// as a list of items and no sampling or output-limit parameters. Everything
// else passes through unchanged.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account == nil || p.Account.Agent != codexID {
		return body
	}
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&m) != nil || m == nil {
		return body
	}
	if s, ok := m["input"].(string); ok {
		m["input"] = []any{map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": s}},
		}}
	}
	for _, k := range []string{"max_output_tokens", "max_tokens", "max_completion_tokens", "temperature", "top_p"} {
		delete(m, k)
	}
	m["stream"] = true
	m["store"] = false
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}
