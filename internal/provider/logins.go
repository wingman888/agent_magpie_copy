package provider

// Remembered logins: the Claude Code and Codex accounts magpie keeps besides
// the one each agent is signed in to.
//
// An agent keeps exactly one login on disk, its current one, and alone holds
// that login's refresh token: a refresh rotates it, so a second holder would
// knock the other out. magpie keeps every other login in accounts.json (mode
// 0600) with credentials that only magpie holds. Switching moves credentials
// from one place to the other, and nothing is ever held twice.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// savedLogin is one remembered account.
type savedLogin struct {
	Agent string         `json:"agent"`
	User  string         `json:"user"`
	Plan  string         `json:"plan,omitempty"`
	On    bool           `json:"on,omitempty"`   // serves requests alongside the current login
	Auth  map[string]any `json:"auth,omitempty"` // the agent's credentials, held by magpie while not current
}

type loginsFile struct {
	Logins []savedLogin `json:"logins"`
}

// Login is a remembered account as `magpie accounts` shows it.
type Login struct {
	Agent   string
	User    string
	Plan    string
	Current bool // the agent is signed in to it
	On      bool // serves requests alongside the current login (the current one always does)
	Held    bool // magpie holds its credentials, so it can be switched to or used
}

// loginsMu serializes reads and writes of accounts.json, and refreshes of the
// tokens magpie holds. Nothing that takes it calls back into code that does.
var loginsMu sync.Mutex

// tokenClient sends the token refreshes. Refreshes run while loginsMu (or the
// Claude/Codex token mutexes) is held, so a request that never answers would
// block every other account; the timeout bounds that wait.
var tokenClient = &http.Client{Timeout: 15 * time.Second}

func loginsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

func loadLogins() loginsFile {
	var f loginsFile
	if b, err := os.ReadFile(loginsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func storeLogins(f loginsFile) error {
	p := loginsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// checkAgent accepts the agents that have remembered logins: claude and codex.
func checkAgent(agent string) error {
	if agent != "claude" && agent != "codex" {
		return fmt.Errorf("accounts are kept for claude and codex, not %q", agent)
	}
	return nil
}

// login is the agent's own sign-in, as the agent keeps it on disk.
type login struct {
	user, plan string
	auth       map[string]any // the credential fields the agent keeps
}

func codexAuthPath(home string) string { return filepath.Join(home, ".codex", "auth.json") }

// claudeCredentialPath is where Claude Code keeps its file-based login.
func claudeCredentialPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// currentLogin reads the sign-in the agent itself is using.
func currentLogin(agent string) (login, bool) {
	switch agent {
	case "claude":
		a, ok := claudeAccount()
		if !ok {
			return login{}, false
		}
		c, _, ok := claudeCredential()
		if !ok {
			return login{}, false
		}
		return login{user: a.Account.User, plan: a.Account.Plan,
			auth: map[string]any{"claudeAiOauth": c.raw["claudeAiOauth"]}}, true
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return login{}, false
		}
		a, ok := codexAccount(home)
		if !ok {
			return login{}, false
		}
		var raw map[string]any
		if !readJSON(codexAuthPath(home), &raw) {
			return login{}, false
		}
		auth := map[string]any{"tokens": raw["tokens"]}
		if m, ok := raw["auth_mode"]; ok {
			auth["auth_mode"] = m
		}
		return login{user: a.Account.User, plan: a.Account.Plan, auth: auth}, true
	}
	return login{}, false
}

// writeCurrent makes the agent's own sign-in the given one. Only the login
// fields change: the rest of the agent's file stays as it was.
func writeCurrent(agent string, auth map[string]any) error {
	switch agent {
	case "claude":
		c, loc, ok := readClaudeCredential()
		if !ok {
			loc = claudeCredentialLocation{path: claudeCredentialPath()}
		}
		raw := c.raw
		if raw == nil {
			raw = map[string]any{}
		}
		oauth, _ := auth["claudeAiOauth"].(map[string]any)
		raw["claudeAiOauth"] = oauth
		var fields claudeAuth
		if b, err := json.Marshal(oauth); err == nil {
			json.Unmarshal(b, &fields)
		}
		return saveClaudeCredential(loc, claudeCredentials{raw: raw, OAuth: fields})
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := codexAuthPath(home)
		// a file that is there but unreadable is not ours to overwrite
		var raw map[string]any
		if b, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(b, &raw); err != nil {
				return fmt.Errorf("%s is not valid JSON; leaving it as it is: %w", path, err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if raw == nil {
			raw = map[string]any{}
		}
		raw["tokens"] = auth["tokens"]
		if m, ok := auth["auth_mode"]; ok {
			raw["auth_mode"] = m
		}
		b, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, append(b, '\n'), 0o600)
	}
	return checkAgent(agent)
}

// forgetCaches drops what was read about the agent's login, after it changed.
func forgetCaches() {
	forgetClaudeCredential()
	forgetClaudeStatus()
}

func findLogin(f loginsFile, agent, user string) int {
	for i, l := range f.Logins {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			return i
		}
	}
	return -1
}

// remember records the agent's current sign-in in f, if it has one, and
// gives its credentials back to the agent alone: magpie drops its own copy.
// A login seen for the first time joins on, when the agent already has another.
func remember(f *loginsFile, agent string) (login, bool) {
	cur, ok := currentLogin(agent)
	if !ok {
		return login{}, false
	}
	i := findLogin(*f, agent, cur.user)
	if i < 0 {
		others := false
		for _, l := range f.Logins {
			others = others || l.Agent == agent
		}
		f.Logins = append(f.Logins, savedLogin{Agent: agent, User: cur.user, Plan: cur.plan, On: others})
		return cur, true
	}
	f.Logins[i].Auth = nil
	if cur.plan != "" {
		f.Logins[i].Plan = cur.plan
	}
	return cur, true
}

// Logins lists the remembered accounts of an agent, the current one marked.
// Logins seen on disk for the first time are remembered on the way.
func Logins(agent string) ([]Login, error) {
	if err := checkAgent(agent); err != nil {
		return nil, err
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	before, _ := json.Marshal(f)
	cur, hasCur := remember(&f, agent)
	if after, _ := json.Marshal(f); !bytes.Equal(before, after) {
		if err := storeLogins(f); err != nil {
			return nil, err
		}
	}
	var out []Login
	for _, l := range f.Logins {
		if l.Agent != agent {
			continue
		}
		current := hasCur && strings.EqualFold(cur.user, l.User)
		out = append(out, Login{Agent: agent, User: l.User, Plan: l.Plan,
			Current: current, On: l.On || current, Held: current || l.Auth != nil})
	}
	return out, nil
}

// SwitchLogin signs the agent in to a remembered account (the email is not
// case-sensitive). The account it replaces keeps its newest credentials, and
// inherits the on state of the one it makes way for.
func SwitchLogin(agent, user string) error {
	if err := checkAgent(agent); err != nil {
		return err
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	cur, hasCur := remember(&f, agent)
	t := findLogin(f, agent, user)
	if t < 0 {
		return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agent, user, agent)
	}
	target := f.Logins[t]
	if hasCur && strings.EqualFold(cur.user, target.User) {
		return nil
	}
	if target.Auth == nil {
		return fmt.Errorf("magpie holds no sign-in for %s; sign in with magpie accounts add %s", target.User, agent)
	}
	if hasCur {
		o := findLogin(f, agent, cur.user)
		f.Logins[o].Auth = cur.auth
		f.Logins[o].On = target.On
	}
	if err := writeCurrent(agent, target.Auth); err != nil {
		return err
	}
	f.Logins[t].Auth = nil
	forgetCaches()
	return storeLogins(f)
}

// ForgetLogin drops a remembered account. The one the agent is signed in to
// cannot be forgotten: sign it out in the agent first.
func ForgetLogin(agent, user string) error {
	if err := checkAgent(agent); err != nil {
		return err
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	cur, hasCur := remember(&f, agent)
	if hasCur && strings.EqualFold(cur.user, user) {
		return fmt.Errorf("%s is the account %s is signed in to; switch to another account first", cur.user, agent)
	}
	i := findLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", agent, user)
	}
	f.Logins = append(f.Logins[:i], f.Logins[i+1:]...)
	return storeLogins(f)
}

// SetLoginOn makes a remembered account serve requests alongside the current
// login (on) or stops it (off). The current login always serves, so turning
// it off is refused. Its credentials are never written into the agent's store.
func SetLoginOn(agent, user string, on bool) error {
	if err := checkAgent(agent); err != nil {
		return err
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	cur, hasCur := remember(&f, agent)
	i := findLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", agent, user)
	}
	if !on && hasCur && strings.EqualFold(cur.user, user) {
		return fmt.Errorf("%s is the account %s is signed in to, so it always serves requests", user, agent)
	}
	f.Logins[i].On = on
	return storeLogins(f)
}

// LoginCommand is the agent's own sign-in command, which opens the vendor's
// login page and waits for the browser to come back.
func LoginCommand(agent string) ([]string, error) {
	switch agent {
	case "claude":
		exe := claudeExecutable()
		if exe == "" {
			return nil, errors.New("claude is not installed; install Claude Code first")
		}
		return []string{exe, "auth", "login"}, nil
	case "codex":
		exe, err := exec.LookPath("codex")
		if err != nil {
			return nil, errors.New("codex is not installed; install Codex CLI first")
		}
		return []string{exe, "login"}, nil
	}
	return nil, checkAgent(agent)
}

// AddLogin signs the agent in with run (the agent's own login), then records
// the account it ends up with. The sign-in the agent had is kept first, so
// it survives the new one taking the agent's file.
func AddLogin(agent string, run func() error) (Login, error) {
	if err := checkAgent(agent); err != nil {
		return Login{}, err
	}
	loginsMu.Lock()
	f := loadLogins()
	var before string // the login being replaced, which stays on
	if prev, ok := remember(&f, agent); ok {
		before = prev.user
		f.Logins[findLogin(f, agent, prev.user)].Auth = prev.auth
	}
	err := storeLogins(f)
	loginsMu.Unlock()
	if err != nil {
		return Login{}, err
	}

	runErr := run()

	loginsMu.Lock()
	defer loginsMu.Unlock()
	forgetCaches()
	f = loadLogins()
	cur, ok := remember(&f, agent)
	if ok && before != "" && !strings.EqualFold(before, cur.user) {
		// the account it displaces keeps serving alongside the new one
		if i := findLogin(f, agent, before); i >= 0 {
			f.Logins[i].On = true
		}
	}
	if err := storeLogins(f); err != nil {
		return Login{}, err
	}
	if runErr != nil {
		return Login{}, runErr
	}
	if !ok {
		return Login{}, fmt.Errorf("no %s sign-in found after signing in", agent)
	}
	entry := f.Logins[findLogin(f, agent, cur.user)]
	return Login{Agent: agent, User: entry.User, Plan: entry.Plan, Current: true, On: entry.On, Held: true}, nil
}

// servingLogins are the logins a request to a signed-in account may use: the
// agent's current one first, then the others the user switched on, in the
// order they were remembered.
func (p Provider) servingLogins() []Provider {
	out := []Provider{p}
	if p.Account == nil {
		return out
	}
	loginsMu.Lock()
	f := loadLogins()
	loginsMu.Unlock()
	for _, l := range f.Logins {
		if l.Agent != p.Account.Agent || !l.On || l.Auth == nil || strings.EqualFold(l.User, p.Account.User) {
			continue
		}
		q := p
		q.Account = savedAccount(l)
		out = append(out, q)
	}
	return out
}

// savedAccount is the account of a remembered login magpie holds the
// credentials of. It signs with them, refreshing them through the vendor.
func savedAccount(l savedLogin) *Account {
	user := l.User
	acct := &Account{Agent: l.Agent, User: l.User, Plan: l.Plan}
	switch l.Agent {
	case "claude":
		acct.sign = claudeSigner(func(ctx context.Context) (string, error) {
			oauth, err := savedClaudeOAuth(ctx, user)
			if err != nil {
				return "", err
			}
			tok, _ := oauth["accessToken"].(string)
			return tok, nil
		})
		acct.body = claudeBody
		acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
		acct.env = func(ctx context.Context) ([]string, func(), error) { return claudeLoginEnv(ctx, user) }
	case "codex":
		acct.Stream = true
		acct.sign = codexSigner(func(ctx context.Context) (string, string, error) { return savedCodexToken(ctx, user) })
		acct.body = codexBody
		acct.models = catalog.Codex
		acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
	}
	return acct
}

// savedClaudeOAuth returns the OAuth fields of a remembered Claude login,
// refreshed first when they are near expiry; the rotated pair is saved back.
func savedClaudeOAuth(ctx context.Context, user string) (map[string]any, error) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	i := findLogin(f, "claude", user)
	if i < 0 || f.Logins[i].Auth == nil {
		return nil, fmt.Errorf("magpie holds no Claude Code sign-in for %s; sign in with magpie accounts add claude", user)
	}
	oauth, _ := f.Logins[i].Auth["claudeAiOauth"].(map[string]any)
	if oauth == nil {
		return nil, fmt.Errorf("magpie holds no Claude Code sign-in for %s; sign in with magpie accounts add claude", user)
	}
	var a claudeAuth
	if b, err := json.Marshal(oauth); err != nil || json.Unmarshal(b, &a) != nil {
		return nil, fmt.Errorf("the Claude Code sign-in kept for %s is unreadable", user)
	}
	if claudeFresh(claudeCredentials{OAuth: a}) {
		return oauth, nil
	}
	if a.RefreshToken == "" {
		return nil, fmt.Errorf("the Claude Code sign-in for %s expired; sign in with magpie accounts add claude", user)
	}
	access, refresh, expiresIn, err := refreshClaude(ctx, a.RefreshToken)
	if err != nil {
		return nil, err
	}
	oauth["accessToken"] = access
	if refresh != "" {
		oauth["refreshToken"] = refresh
	}
	if expiresIn > 0 {
		oauth["expiresAt"] = time.Now().Add(time.Duration(expiresIn) * time.Second).UnixMilli()
	}
	f.Logins[i].Auth["claudeAiOauth"] = oauth
	if err := storeLogins(f); err != nil {
		return nil, err
	}
	return oauth, nil
}

// refreshClaude trades a Claude refresh token for a new access token.
func refreshClaude(ctx context.Context, refresh string) (access, newRefresh string, expiresIn int64, err error) {
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh,
		"client_id": claudeClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := tokenClient.Do(req)
	if err != nil {
		return "", "", 0, errors.New("Claude Code token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return "", "", 0, errors.New("Claude Code is signed out (token refresh failed); sign in again")
	}
	return fresh.AccessToken, fresh.RefreshToken, fresh.ExpiresIn, nil
}

// claudeLoginEnv runs Claude Code with a remembered login: its credentials go
// into a private temporary config directory, removed by the cleanup. The
// agent's own store is never touched.
func claudeLoginEnv(ctx context.Context, user string) ([]string, func(), error) {
	oauth, err := savedClaudeOAuth(ctx, user)
	if err != nil {
		return nil, func() {}, err
	}
	dir, err := os.MkdirTemp("", "magpie-claude-login-")
	if err != nil {
		return nil, func() {}, err
	}
	done := func() { os.RemoveAll(dir) }
	b, _ := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), b, 0o600); err != nil {
		done()
		return nil, func() {}, err
	}
	return []string{"CLAUDE_CONFIG_DIR=" + dir}, done, nil
}

// savedCodexToken returns the access token and account id of a remembered
// Codex login, refreshed first when near expiry; the rotated tokens are saved back.
func savedCodexToken(ctx context.Context, user string) (tok, accountID string, err error) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	i := findLogin(f, "codex", user)
	if i < 0 || f.Logins[i].Auth == nil {
		return "", "", fmt.Errorf("magpie holds no Codex sign-in for %s; sign in with magpie accounts add codex", user)
	}
	tokens, _ := f.Logins[i].Auth["tokens"].(map[string]any)
	if tokens == nil {
		return "", "", fmt.Errorf("the Codex sign-in kept for %s is unreadable", user)
	}
	access, _ := tokens["access_token"].(string)
	refresh, _ := tokens["refresh_token"].(string)
	idToken, _ := tokens["id_token"].(string)
	accountID, _ = tokens["account_id"].(string)
	if accountID == "" {
		accountID = claimString(jwtClaims(idToken), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(access)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
		return access, accountID, nil
	}
	if refresh == "" {
		return "", "", fmt.Errorf("the Codex sign-in for %s expired; sign in with magpie accounts add codex", user)
	}
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": refresh, "scope": "openid profile email"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := tokenClient.Do(req)
	if err != nil {
		return "", "", errors.New("Codex token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return "", "", errors.New("Codex is signed out (token refresh failed); sign in again")
	}
	tokens["access_token"] = fresh.AccessToken
	if fresh.IDToken != "" {
		tokens["id_token"] = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		tokens["refresh_token"] = fresh.RefreshToken
	}
	f.Logins[i].Auth["tokens"] = tokens
	if err := storeLogins(f); err != nil {
		return "", "", err
	}
	return fresh.AccessToken, accountID, nil
}
