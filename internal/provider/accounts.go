package provider

// Remembered sign-ins: the Claude Code and Codex accounts the user has been
// signed in to, so they can switch between them, keep more than one working,
// and see each one's quota.
//
// One holder per refresh token. The account an agent is signed in to keeps
// its tokens in the agent's own credential store and nowhere else. Every other
// remembered account keeps its tokens in accounts.json beside providers.json
// (mode 0600), and magpie refreshes them there. Switching moves the tokens
// between the two places, so a rotated token is never left behind.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// storedAccount is one remembered sign-in. Creds is set only while the
// account is not the one the agent is signed in to: the agent's credential
// blob for claude (claudeAiOauth), the tokens object for codex.
type storedAccount struct {
	Agent string          `json:"agent"`
	User  string          `json:"user"`
	Plan  string          `json:"plan,omitempty"`
	On    bool            `json:"on,omitempty"` // keeps serving as a candidate while it is not signed in
	Creds json.RawMessage `json:"credentials,omitempty"`
}

type accountFile struct {
	Accounts []storedAccount `json:"accounts"`
}

var (
	accountsMu      sync.Mutex // guards accounts.json
	storedRefreshMu sync.Mutex // one refresh of a remembered account at a time
)

// Login is one remembered sign-in as the CLI shows it.
type Login struct {
	Agent      string
	User       string
	Plan       string
	Current    bool // the agent is signed in to it now
	On         bool // serves as a candidate alongside the signed-in one
	NeedsLogin bool // not signed in, and magpie holds no tokens for it
}

// AgentID maps what the user typed to an agent magpie can remember sign-ins
// for: claude (or cc) and codex.
func AgentID(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func accountsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

func loadAccounts() accountFile {
	var f accountFile
	if b, err := os.ReadFile(accountsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

// storeAccounts writes accounts.json atomically, readable by the user only.
func storeAccounts(f accountFile) error {
	p := accountsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// editAccounts runs fn on the saved sign-ins under the lock. The file is
// written only when fn reports a change and does not fail.
func editAccounts(fn func(f *accountFile) (bool, error)) error {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	changed, err := fn(&f)
	if err != nil || !changed {
		return err
	}
	return storeAccounts(f)
}

// storedIndex finds a remembered sign-in by agent and email, ignoring case.
func storedIndex(f *accountFile, agent, user string) int {
	for i, a := range f.Accounts {
		if a.Agent == agent && strings.EqualFold(a.User, user) {
			return i
		}
	}
	return -1
}

func countAgent(f *accountFile, agent string) int {
	n := 0
	for _, a := range f.Accounts {
		if a.Agent == agent {
			n++
		}
	}
	return n
}

// storedBlob returns a copy of the tokens magpie holds for an account.
func storedBlob(agent, user string) (json.RawMessage, bool) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := storedIndex(&f, agent, user)
	if i < 0 || f.Accounts[i].Creds == nil {
		return nil, false
	}
	return append(json.RawMessage(nil), f.Accounts[i].Creds...), true
}

// updateStored replaces the tokens magpie holds for an account.
func updateStored(agent, user string, blob json.RawMessage) error {
	return editAccounts(func(f *accountFile) (bool, error) {
		i := storedIndex(f, agent, user)
		if i < 0 {
			return false, fmt.Errorf("no saved %s account %q", agent, user)
		}
		f.Accounts[i].Creds = blob
		return true, nil
	})
}

// currentLogin is the account the agent is signed in to, if any.
func currentLogin(agent string) (user, plan string, ok bool) {
	switch agent {
	case "claude":
		if p, ok := claudeAccount(); ok {
			return p.Account.User, p.Account.Plan, true
		}
	case "codex":
		if home, err := os.UserHomeDir(); err == nil {
			if p, ok := codexAccount(home); ok {
				return p.Account.User, p.Account.Plan, true
			}
		}
	}
	return "", "", false
}

// rememberLogins records the account each agent is signed in to, and makes
// sure it holds no tokens of its own: the agent does. A second account
// recorded this way is on by default.
func rememberLogins() {
	for _, agent := range []string{"claude", "codex"} {
		user, plan, ok := currentLogin(agent)
		if !ok || !strings.Contains(user, "@") {
			continue
		}
		_ = editAccounts(func(f *accountFile) (bool, error) {
			i := storedIndex(f, agent, user)
			if i < 0 {
				f.Accounts = append(f.Accounts, storedAccount{Agent: agent, User: user, Plan: plan,
					On: countAgent(f, agent) > 0})
				return true, nil
			}
			a := &f.Accounts[i]
			changed := false
			if a.Creds != nil {
				a.Creds, changed = nil, true
			}
			if plan != "" && a.Plan != plan {
				a.Plan, changed = plan, true
			}
			return changed, nil
		})
	}
}

// Logins lists the remembered sign-ins of an agent, or of every agent when
// agent is "".
func Logins(agent string) []Login {
	rememberLogins()
	signedIn := map[string]string{}
	for _, a := range []string{"claude", "codex"} {
		if u, _, ok := currentLogin(a); ok {
			signedIn[a] = u
		}
	}
	accountsMu.Lock()
	f := loadAccounts()
	accountsMu.Unlock()
	var out []Login
	for _, a := range f.Accounts {
		if agent != "" && a.Agent != agent {
			continue
		}
		cur := signedIn[a.Agent] != "" && strings.EqualFold(signedIn[a.Agent], a.User)
		out = append(out, Login{Agent: a.Agent, User: a.User, Plan: a.Plan, Current: cur,
			On: a.On || cur, NeedsLogin: !cur && a.Creds == nil})
	}
	return out
}

// SetLoginOn turns a remembered sign-in's candidate role on or off. The
// account the agent is signed in to is always on.
func SetLoginOn(agent, user string, on bool) error {
	id, ok := AgentID(agent)
	if !ok {
		return fmt.Errorf("unknown agent %q; magpie accounts supports claude (cc) and codex", agent)
	}
	rememberLogins()
	cur, _, signedIn := currentLogin(id)
	return editAccounts(func(f *accountFile) (bool, error) {
		i := storedIndex(f, id, user)
		if i < 0 {
			return false, fmt.Errorf("no saved %s account %q; magpie accounts %s lists them", id, user, id)
		}
		a := &f.Accounts[i]
		if !on && signedIn && strings.EqualFold(cur, a.User) {
			return false, fmt.Errorf("%s is the account %s is signed in to now; it stays on", a.User, id)
		}
		if a.On == on {
			return false, nil
		}
		a.On = on
		return true, nil
	})
}

// readLogin returns the agent's own credentials for the account it is signed
// in to, in the form magpie keeps them.
func readLogin(agent string) (json.RawMessage, bool) {
	switch agent {
	case "claude":
		raw, _ := claudeRawLogin()
		o, ok := raw["claudeAiOauth"].(map[string]any)
		if !ok {
			return nil, false
		}
		b, err := json.Marshal(o)
		return b, err == nil
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		raw, _ := codexRawLogin(home)
		t, ok := raw["tokens"].(map[string]any)
		if !ok {
			return nil, false
		}
		b, err := json.Marshal(t)
		return b, err == nil
	}
	return nil, false
}

// writeLogin signs the agent in with the given credentials. Every other field
// of the agent's own file is kept as it was.
func writeLogin(agent string, blob json.RawMessage) error {
	switch agent {
	case "claude":
		raw, loc := claudeRawLogin()
		var o map[string]any
		var auth claudeAuth
		if json.Unmarshal(blob, &o) != nil || json.Unmarshal(blob, &auth) != nil {
			return errors.New("remembered Claude sign-in is unreadable")
		}
		raw["claudeAiOauth"] = o
		if !loc.keychain {
			if err := os.MkdirAll(filepath.Dir(loc.path), 0o755); err != nil {
				return err
			}
		}
		err := saveClaudeCredential(loc, claudeCredentials{raw: raw, OAuth: auth})
		forgetClaudeCredential()
		forgetClaudeStatus()
		return err
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		raw, path := codexRawLogin(home)
		var t map[string]any
		if json.Unmarshal(blob, &t) != nil {
			return errors.New("remembered Codex sign-in is unreadable")
		}
		raw["tokens"] = t
		if _, ok := raw["auth_mode"]; !ok {
			raw["auth_mode"] = "chatgpt"
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		b, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(path, append(b, '\n'), 0o600)
	}
	return fmt.Errorf("unknown agent %q", agent)
}

// claudeRawLogin reads Claude Code's credential file as a whole, so that
// writing it back keeps the fields magpie does not own. The location is
// returned so the same store is written to.
func claudeRawLogin() (map[string]any, claudeCredentialLocation) {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	loc := claudeCredentialLocation{path: filepath.Join(dir, ".credentials.json")}
	raw := map[string]any{}
	if b, err := os.ReadFile(loc.path); err == nil {
		if json.Unmarshal(b, &raw) != nil || raw == nil {
			raw = map[string]any{}
		}
		return raw, loc
	}
	if claudeKeychain {
		if c, kloc, ok := readClaudeCredential(); ok {
			return c.raw, kloc
		}
		return raw, claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
	}
	return raw, loc
}

// codexRawLogin reads Codex CLI's auth.json as a whole, for the same reason.
func codexRawLogin(home string) (map[string]any, string) {
	path := filepath.Join(home, ".codex", "auth.json")
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &raw) != nil || raw == nil {
			raw = map[string]any{}
		}
	}
	return raw, path
}

// SwitchLogin signs the agent in to a remembered account. The account being
// replaced keeps its latest tokens and takes over the target's on/off state.
// Sessions that are already running keep the old account until restarted.
func SwitchLogin(agent, user string) error {
	id, ok := AgentID(agent)
	if !ok {
		return fmt.Errorf("unknown agent %q; magpie accounts supports claude (cc) and codex", agent)
	}
	rememberLogins()
	cur, _, signedIn := currentLogin(id)
	if signedIn && strings.EqualFold(cur, user) {
		return nil
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := storedIndex(&f, id, user)
	if i < 0 {
		return fmt.Errorf("no saved %s account %q; magpie accounts %s lists them", id, user, id)
	}
	target := f.Accounts[i]
	if target.Creds == nil {
		return fmt.Errorf("magpie holds no sign-in for %s; sign in to it with magpie accounts add %s", target.User, id)
	}
	// Take the outgoing tokens into the store before the agent's file changes.
	out := -1
	var outOn bool
	if signedIn {
		if b, ok := readLogin(id); ok {
			if j := storedIndex(&f, id, cur); j >= 0 {
				out, outOn = j, f.Accounts[j].On
				f.Accounts[j].Creds, f.Accounts[j].On = b, target.On
			}
		}
	}
	if err := storeAccounts(f); err != nil {
		return err
	}
	if err := writeLogin(id, target.Creds); err != nil {
		// the agent still holds its own tokens; the store gives them back
		if out >= 0 {
			f.Accounts[out].Creds, f.Accounts[out].On = nil, outOn
			_ = storeAccounts(f)
		}
		return err
	}
	f.Accounts[i].Creds = nil
	return storeAccounts(f)
}

// ForgetLogin drops a remembered account. The one the agent is signed in to
// cannot be forgotten.
func ForgetLogin(agent, user string) error {
	id, ok := AgentID(agent)
	if !ok {
		return fmt.Errorf("unknown agent %q; magpie accounts supports claude (cc) and codex", agent)
	}
	rememberLogins()
	cur, _, signedIn := currentLogin(id)
	if signedIn && strings.EqualFold(cur, user) {
		return fmt.Errorf("%s is signed in to %s now; sign out of it in %s before forgetting it", user, id, id)
	}
	return editAccounts(func(f *accountFile) (bool, error) {
		i := storedIndex(f, id, user)
		if i < 0 {
			return false, fmt.Errorf("no saved %s account %q; magpie accounts %s lists them", id, user, id)
		}
		f.Accounts = append(f.Accounts[:i], f.Accounts[i+1:]...)
		return true, nil
	})
}

// loginCommand is the agent's own sign-in command, which opens the vendor's
// login page. A var so tests can stand in for the browser.
var loginCommand = func(agent string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	switch agent {
	case "claude":
		path := claudeExecutable()
		if path == "" {
			return nil, errors.New("Claude Code is not installed; install it, then run claude auth login")
		}
		cmd = exec.Command(path, "auth", "login")
	case "codex":
		path, err := exec.LookPath("codex")
		if err != nil {
			return nil, errors.New("Codex CLI is not installed; install it, then run codex login")
		}
		cmd = exec.Command(path, "login")
	default:
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd, nil
}

// AddLogin signs the agent in through its own login page, then remembers the
// account it ends up with. It returns that account's email.
func AddLogin(agent string) (string, error) {
	id, ok := AgentID(agent)
	if !ok {
		return "", fmt.Errorf("unknown agent %q; magpie accounts supports claude (cc) and codex", agent)
	}
	rememberLogins()
	cur, _, signedIn := currentLogin(id)
	if signedIn {
		// the login replaces the agent's tokens; keep the outgoing account's
		if b, ok := readLogin(id); ok {
			_ = editAccounts(func(f *accountFile) (bool, error) {
				j := storedIndex(f, id, cur)
				if j < 0 {
					return false, nil
				}
				f.Accounts[j].Creds = b
				return true, nil
			})
		}
	}
	cmd, err := loginCommand(id)
	if err != nil {
		rememberLogins()
		return "", err
	}
	runErr := cmd.Run()
	forgetClaudeCredential()
	forgetClaudeStatus()
	rememberLogins()
	user, _, ok := currentLogin(id)
	if runErr != nil {
		return "", fmt.Errorf("%s sign-in did not finish: %v", id, runErr)
	}
	if !ok {
		return "", fmt.Errorf("%s is still signed out", id)
	}
	return user, nil
}

// accountSlots lists the candidates for a signed-in account: the account the
// agent is signed in to first, then the other remembered accounts that are on,
// in the order they were saved. Each carries its own tokens. Only Codex
// accounts take part; Claude Code requests run through the Claude Code binary,
// which can only use the account it is signed in to.
func (p Provider) accountSlots() []Provider {
	out := []Provider{p}
	if p.Account == nil || p.Account.Agent != "codex" {
		return out
	}
	cur, _, _ := currentLogin("codex")
	accountsMu.Lock()
	f := loadAccounts()
	accountsMu.Unlock()
	for _, a := range f.Accounts {
		if a.Agent != "codex" || !a.On || a.Creds == nil || strings.EqualFold(a.User, cur) {
			continue
		}
		c := p
		c.ID = p.ID + "-" + Slug(a.User)
		c.Account = storedCodexAccount(a.User, a.Plan)
		c.Key, c.KeyName, c.Keys, c.Fallback, c.Models = "", "", nil, nil, nil
		out = append(out, c)
	}
	return out
}

// storedCodexAccount is a remembered Codex account, signing requests with its
// own tokens.
func storedCodexAccount(user, plan string) *Account {
	acct := &Account{Agent: "codex", Stream: true, User: user, Plan: plan}
	acct.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
		tok, accountID, err := storedCodexToken(ctx, user)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if accountID != "" {
			req.Header.Set("chatgpt-account-id", accountID)
		}
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "magpie")
		return nil
	}
	acct.body = codexBody
	acct.models = catalog.Codex
	acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
	return acct
}

// storedCodexToken returns a usable access token for a remembered Codex
// account, refreshing it when it is about to expire.
func storedCodexToken(ctx context.Context, user string) (string, string, error) {
	storedRefreshMu.Lock()
	defer storedRefreshMu.Unlock()
	blob, ok := storedBlob("codex", user)
	if !ok {
		return "", "", fmt.Errorf("magpie holds no sign-in for %s; sign in to it with magpie accounts add codex", user)
	}
	var t map[string]any
	if json.Unmarshal(blob, &t) != nil || t == nil {
		return "", "", fmt.Errorf("remembered Codex sign-in for %s is unreadable", user)
	}
	str := func(k string) string { s, _ := t[k].(string); return s }
	access, refresh := str("access_token"), str("refresh_token")
	accountID := str("account_id")
	if accountID == "" {
		accountID = claimString(jwtClaims(str("id_token")), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(access)["exp"].(float64); access != "" && (exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute) {
		return access, accountID, nil
	}
	if refresh == "" {
		return "", "", fmt.Errorf("%s's Codex sign-in expired; sign in to it with magpie accounts add codex", user)
	}
	g, err := codexRefresh(ctx, refresh)
	if err != nil {
		return "", "", err
	}
	t["access_token"] = g.AccessToken
	if g.IDToken != "" {
		t["id_token"] = g.IDToken
	}
	if g.RefreshToken != "" {
		t["refresh_token"] = g.RefreshToken
	}
	out, _ := json.Marshal(t)
	if err := updateStored("codex", user, out); err != nil {
		return "", "", err
	}
	return g.AccessToken, accountID, nil
}

// storedClaudeToken returns a usable access token for a remembered Claude
// account, refreshing it when it is about to expire.
func storedClaudeToken(ctx context.Context, user string) (string, error) {
	storedRefreshMu.Lock()
	defer storedRefreshMu.Unlock()
	blob, ok := storedBlob("claude", user)
	if !ok {
		return "", fmt.Errorf("magpie holds no sign-in for %s; sign in to it with magpie accounts add claude", user)
	}
	var a claudeAuth
	var o map[string]any
	if json.Unmarshal(blob, &a) != nil || json.Unmarshal(blob, &o) != nil || o == nil {
		return "", fmt.Errorf("remembered Claude sign-in for %s is unreadable", user)
	}
	if a.AccessToken != "" && (a.ExpiresAt == 0 || time.Until(claudeExpiry(a.ExpiresAt)) > 5*time.Minute) {
		return a.AccessToken, nil
	}
	if a.RefreshToken == "" {
		return "", fmt.Errorf("%s's Claude sign-in expired; sign in to it with magpie accounts add claude", user)
	}
	g, err := claudeRefresh(ctx, a.RefreshToken)
	if err != nil {
		return "", err
	}
	o["accessToken"] = g.AccessToken
	if g.RefreshToken != "" {
		o["refreshToken"] = g.RefreshToken
	}
	if g.ExpiresIn > 0 {
		o["expiresAt"] = time.Now().Add(time.Duration(g.ExpiresIn) * time.Second).UnixMilli()
	}
	out, _ := json.Marshal(o)
	if err := updateStored("claude", user, out); err != nil {
		return "", err
	}
	return g.AccessToken, nil
}
