package provider

// Account providers borrow the sign-in an agent already keeps on this machine
// (Codex's ChatGPT login, Copilot's GitHub login). dial never copies those
// credentials: each use reads the agent's own file, so when the agent signs
// out the provider disappears, and when the agent refreshes a token dial sees
// the new one. Only the chosen model list is ever written to dial's config.

import (
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

// Account says whose subscription an account provider uses.
type Account struct {
	Agent string `json:"agent"` // "codex" or "copilot"
	User  string `json:"user"`  // account name shown to the user
	Plan  string `json:"plan"`  // subscription tier, when the agent says (may be empty)
}

// Exclusion is a sign-in dial recognises but does not offer as a provider.
// It carries no credentials.
type Exclusion struct {
	Agent string `json:"agent"`
	Why   string `json:"why"`
}

// ErrSignedOut is wrapped by errors returned when an account's sign-in is gone
// or rejected, so callers can tell the user to sign in again.
var ErrSignedOut = errors.New("signed out")

// CodexBase is the ChatGPT backend Codex requests go to. A variable so tests
// can point it at a stand-in server.
var CodexBase = "https://chatgpt.com/backend-api/codex"

// CopilotTokenURL is where a GitHub OAuth token is exchanged for a short-lived
// Copilot session token. A variable so tests can point it at a stand-in.
var CopilotTokenURL = "https://api.github.com/copilot_internal/v2/token"

const claudeWhy = "Anthropic's terms only allow a Claude subscription to be used by Claude Code itself, " +
	"so dial does not offer it as a provider. To use Claude models through dial, add an " +
	"Anthropic API key as a provider instead."

// copilotUA identifies dial's Copilot requests the way the editor plugin does.
const (
	copilotIntegration = "vscode-chat"
	copilotEditor      = "vscode/1.99.3"
	copilotPlugin      = "copilot-chat/0.26.7"
	copilotUA          = "GitHubCopilotChat/0.26.7"
)

var accountHTTP = &http.Client{Timeout: 30 * time.Second}

// ---- reading the agents' sign-ins -------------------------------------------

// homeFile is a path under the user's home directory.
func homeFile(parts ...string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(append([]string{home}, parts...)...)
}

type codexCreds struct {
	AccessToken string
	Refresh     string
	AccountID   string
	Email       string
	Plan        string
}

type codexAuthFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AccountID    string `json:"account_id"`
	} `json:"tokens"`
}

// codexLogin reads ~/.codex/auth.json. It counts only ChatGPT sign-ins: API-key
// mode, or a file without an access token, is not an account.
func codexLogin() (codexCreds, bool) {
	b, err := os.ReadFile(homeFile(".codex", "auth.json"))
	if err != nil {
		return codexCreds{}, false
	}
	var f codexAuthFile
	if json.Unmarshal(b, &f) != nil {
		return codexCreds{}, false
	}
	if f.AuthMode == "apikey" || f.Tokens.AccessToken == "" {
		return codexCreds{}, false
	}
	c := codexCreds{AccessToken: f.Tokens.AccessToken, Refresh: f.Tokens.RefreshToken, AccountID: f.Tokens.AccountID}
	claims := jwtClaims(f.Tokens.IDToken)
	c.Email, _ = claims["email"].(string)
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		c.Plan, _ = auth["chatgpt_plan_type"].(string)
	}
	return c, true
}

// jwtClaims returns the payload of a JWT without verifying it: dial only reads
// the display fields, and the agent already trusts its own token.
func jwtClaims(tok string) map[string]any {
	parts := strings.Split(tok, ".")
	if len(parts) < 2 {
		return nil
	}
	seg, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(seg, &m) != nil {
		return nil
	}
	return m
}

type copilotCreds struct {
	User  string
	OAuth string
}

type copilotEntry struct {
	User  string `json:"user"`
	OAuth string `json:"oauth_token"`
}

// copilotLogin reads the GitHub entry from the Copilot CLI or editor plugin
// files under $XDG_CONFIG_HOME/github-copilot (default ~/.config).
func copilotLogin() (copilotCreds, bool) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = homeFile(".config")
	}
	dir := filepath.Join(base, "github-copilot")
	for _, name := range []string{"apps.json", "hosts.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var m map[string]copilotEntry
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		var keys []string
		for k := range m {
			if strings.HasPrefix(k, "github.com") {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if e := m[k]; e.OAuth != "" {
				return copilotCreds{User: strings.TrimSpace(e.User), OAuth: e.OAuth}, true
			}
		}
	}
	return copilotCreds{}, false
}

// claudeCredsFile is where Claude Code keeps its login on Linux and Windows.
// Its contents are never read: dial only asks whether the file exists.
func claudeCredsFile() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = homeFile(".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

var claudeKeychain struct {
	sync.Mutex
	at  time.Time
	has bool
}

// claudeKeychainItem reports whether macOS holds a Claude Code login item. It
// asks for the item without -w, so the secret is neither printed nor requested,
// and the answer is kept for a minute to avoid running the tool on every call.
func claudeKeychainItem() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	claudeKeychain.Lock()
	defer claudeKeychain.Unlock()
	if time.Since(claudeKeychain.at) < time.Minute {
		return claudeKeychain.has
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials").Run()
	claudeKeychain.has = err == nil
	claudeKeychain.at = time.Now()
	return claudeKeychain.has
}

// ---- what dial offers --------------------------------------------------------

// ownProvider reports whether the user has a provider of this id with its own
// address; such a provider wins over the account of the same id.
func ownProvider(id string) bool {
	for _, s := range load().Providers {
		if s.ID == id && (s.Chat != "" || s.Responses != "" || s.Anthropic != "") {
			return true
		}
	}
	return false
}

// accountProvider builds the provider for an account, keeping the model picks
// the user saved under its id. Those picks are the only thing dial stores.
func accountProvider(id, name string, acc Account) Provider {
	p := Provider{ID: id, Name: name, Account: &acc}
	for _, s := range load().Providers {
		if s.ID == id {
			p.Models = cleanList(s.Models)
		}
	}
	return p
}

// Accounts lists the signed-in agent accounts dial offers as providers. They
// are ready without a key, and they vanish when the agent signs out.
func Accounts() []Provider {
	var out []Provider
	if c, ok := codexLogin(); ok && !ownProvider("codex") {
		user := c.Email
		if user == "" {
			user = "ChatGPT"
		}
		out = append(out, accountProvider("codex", "Codex", Account{Agent: "codex", User: user, Plan: c.Plan}))
	}
	if c, ok := copilotLogin(); ok && !ownProvider("copilot") {
		user := c.User
		if user == "" {
			user = "GitHub"
		}
		out = append(out, accountProvider("copilot", "Copilot", Account{Agent: "copilot", User: user}))
	}
	return out
}

// excludedCache keeps the last Excluded answer for a minute. It is keyed by the
// Claude credentials path, so a different config directory is never served a
// stale answer. Accounts() is never cached: signing out must take effect at once.
var excludedCache struct {
	sync.Mutex
	key string
	at  time.Time
	out []Exclusion
}

// Excluded lists sign-ins dial recognises but does not offer. Claude Code's
// login is only detected, never read or used.
func Excluded() []Exclusion {
	key := claudeCredsFile()
	excludedCache.Lock()
	defer excludedCache.Unlock()
	if excludedCache.key == key && time.Since(excludedCache.at) < time.Minute {
		return append([]Exclusion(nil), excludedCache.out...)
	}
	var out []Exclusion
	if fileExists(key) || claudeKeychainItem() {
		out = append(out, Exclusion{Agent: "claude", Why: claudeWhy})
	}
	excludedCache.key, excludedCache.at, excludedCache.out = key, time.Now(), out
	return append([]Exclusion(nil), out...)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ---- signing requests ---------------------------------------------------------

// Sign authorises a request bound for this provider. A plain provider uses its
// key. An account provider uses the agent's token and, for Copilot, rewrites
// the URL to the session's endpoint.
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
	return fmt.Errorf("%s: unknown account kind %q", p.Name, p.Account.Agent)
}

// Prepare adjusts a request body for the provider's backend. Plain providers
// get it back unchanged; a Codex account gets the ChatGPT backend's rules.
func (p Provider) Prepare(body []byte) []byte {
	if p.Account != nil && p.Account.Agent == "codex" {
		return prepareCodex(body)
	}
	return body
}

func signCodex(ctx context.Context, req *http.Request) error {
	c, err := codexToken(ctx)
	if err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimRight(CodexBase, "/") + "/responses")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	if c.AccountID != "" {
		req.Header.Set("chatgpt-account-id", c.AccountID)
	}
	return nil
}

func signCopilot(ctx context.Context, req *http.Request, body []byte) error {
	c, ok := copilotLogin()
	if !ok {
		return fmt.Errorf("%w: copilot — sign in to GitHub Copilot again", ErrSignedOut)
	}
	tok, api, err := copilotSession(ctx, c.OAuth)
	if err != nil {
		return err
	}
	u, err := url.Parse(strings.TrimRight(api, "/") + "/chat/completions")
	if err != nil {
		return err
	}
	req.URL = u
	req.Host = u.Host
	in := inspectChat(body)
	h := req.Header
	h.Set("Authorization", "Bearer "+tok)
	h.Set("Copilot-Integration-Id", copilotIntegration)
	h.Set("Editor-Version", copilotEditor)
	h.Set("Editor-Plugin-Version", copilotPlugin)
	h.Set("User-Agent", copilotUA)
	h.Set("Openai-Intent", "conversation-panel")
	h.Set("X-Initiator", in.initiator)
	if in.vision {
		h.Set("Copilot-Vision-Request", "true")
	} else {
		h.Del("Copilot-Vision-Request")
	}
	return nil
}

type chatInfo struct {
	initiator string // "user" when the last message is the user's, else "agent"
	vision    bool   // a message carries an image part
}

// inspectChat reads the chat request just enough to label it for Copilot.
func inspectChat(body []byte) chatInfo {
	var v struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	info := chatInfo{initiator: "agent"}
	if json.Unmarshal(body, &v) != nil {
		return info
	}
	if n := len(v.Messages); n > 0 && v.Messages[n-1].Role == "user" {
		info.initiator = "user"
	}
	for _, m := range v.Messages {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.Type == "image_url" || p.Type == "input_image" || p.Type == "image" {
				info.vision = true
			}
		}
	}
	return info
}

// ---- Copilot session tokens ---------------------------------------------------

type copilotSess struct {
	token string
	api   string
	exp   time.Time
}

var (
	copilotMu       sync.Mutex
	copilotSessions = map[string]copilotSess{} // by OAuth token; held in memory only
)

// copilotSession returns a session token for the OAuth token, reusing a cached
// one until it is within five minutes of expiry.
func copilotSession(ctx context.Context, oauth string) (string, string, error) {
	copilotMu.Lock()
	defer copilotMu.Unlock()
	if s, ok := copilotSessions[oauth]; ok && time.Until(s.exp) > 5*time.Minute {
		return s.token, s.api, nil
	}
	s, err := exchangeCopilot(ctx, oauth)
	if err != nil {
		return "", "", err
	}
	copilotSessions[oauth] = s
	return s.token, s.api, nil
}

func exchangeCopilot(ctx context.Context, oauth string) (copilotSess, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CopilotTokenURL, nil)
	if err != nil {
		return copilotSess{}, err
	}
	req.Header.Set("Authorization", "token "+oauth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUA)
	res, err := accountHTTP.Do(req)
	if err != nil {
		return copilotSess{}, fmt.Errorf("copilot: token exchange: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return copilotSess{}, fmt.Errorf("%w: copilot — GitHub rejected the sign-in; sign in to Copilot again", ErrSignedOut)
	}
	if res.StatusCode/100 != 2 {
		return copilotSess{}, fmt.Errorf("copilot: token exchange: %s", APIError(b, res.Status))
	}
	var v struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &v); err != nil || v.Token == "" || v.Endpoints.API == "" {
		return copilotSess{}, errors.New("copilot: token exchange returned an unexpected response")
	}
	return copilotSess{token: v.Token, api: v.Endpoints.API, exp: time.Unix(v.ExpiresAt, 0)}, nil
}
