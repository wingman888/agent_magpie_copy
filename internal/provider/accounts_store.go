package provider

// Remembered accounts: every Claude Code and Codex sign-in magpie has seen,
// so the user can switch between their subscriptions and let several of them
// serve at once. The file is accounts.json next to providers.json (mode
// 0600) and never leaves the machine.
//
// Credentials have a single holder. While an account is the agent's current
// login, the agent's own file holds it and the agent refreshes it; the copy
// kept here is only a snapshot, and magpie never refreshes it. An account
// that is not signed in to the agent is refreshed from its own copy here.

import (
	"bytes"
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

	"github.com/yetone/magpie/internal/catalog"
)

// ErrUnknownAgent is returned for an agent accounts cannot hold.
var ErrUnknownAgent = errors.New("unknown agent")

// rememberedAccount is one account magpie keeps.
type rememberedAccount struct {
	Agent string          `json:"agent"`
	User  string          `json:"user"`
	Plan  string          `json:"plan,omitempty"`
	On    bool            `json:"on,omitempty"` // 同时生效: served alongside the current login
	Creds json.RawMessage `json:"creds,omitempty"`
}

type accountsFile struct {
	Accounts []rememberedAccount `json:"accounts"`
}

var (
	accountsMu sync.Mutex // guards accounts.json
	recordMu   sync.Mutex // serializes refreshes of remembered accounts
)

// accountsPath is accounts.json, beside providers.json.
func accountsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

func loadAccounts() accountsFile {
	var f accountsFile
	if b, err := os.ReadFile(accountsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func storeAccounts(f accountsFile) error {
	p := accountsPath()
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

// editAccounts applies change to the remembered accounts under the lock. The
// file is written only when change reports a change.
func editAccounts(change func(f *accountsFile) bool) error {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	if !change(&f) {
		return nil
	}
	return storeAccounts(f)
}

func findRemembered(agent, user string) (rememberedAccount, bool) {
	for _, r := range loadAccounts().Accounts {
		if r.Agent == agent && sameUser(r.User, user) {
			return r, true
		}
	}
	return rememberedAccount{}, false
}

func sameUser(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

// compactJSON drops insignificant whitespace, so the same credentials compare
// equal however the file they were stored in was indented.
func compactJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return append(json.RawMessage(nil), b...)
	}
	return buf.Bytes()
}

// accountAgent maps the agents accounts can hold to their ids.
func accountAgent(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("%w %q", ErrUnknownAgent, name)
}

func agentLabel(agent string) string {
	if agent == "codex" {
		return "Codex"
	}
	return "Claude Code"
}

// remember records a sign-in Accounts has just read, and refreshes the copy
// of its credentials. The first account of an agent is not 同时生效; any
// account recorded after it is.
func remember(a *Account) {
	if a == nil || strings.TrimSpace(a.User) == "" {
		return
	}
	creds := compactJSON(a.creds)
	editAccounts(func(f *accountsFile) bool {
		others, idx := 0, -1
		for i, r := range f.Accounts {
			if r.Agent != a.Agent {
				continue
			}
			if sameUser(r.User, a.User) {
				idx = i
			} else {
				others++
			}
		}
		if idx < 0 {
			f.Accounts = append(f.Accounts, rememberedAccount{Agent: a.Agent, User: a.User, Plan: a.Plan,
				On: others > 0, Creds: creds})
			return true
		}
		r := &f.Accounts[idx]
		changed := false
		if a.Plan != "" && r.Plan != a.Plan {
			r.Plan, changed = a.Plan, true
		}
		if creds != nil && !bytes.Equal(compactJSON(r.Creds), creds) {
			r.Creds, changed = creds, true
		}
		return changed
	})
}

// setRememberedCreds stores a refreshed credential file for a remembered account.
func setRememberedCreds(agent, user string, creds []byte) error {
	return editAccounts(func(f *accountsFile) bool {
		for i := range f.Accounts {
			if f.Accounts[i].Agent == agent && sameUser(f.Accounts[i].User, user) {
				f.Accounts[i].Creds = compactJSON(creds)
				return true
			}
		}
		return false
	})
}

// currentLogin is the account the agent is signed in as, or nil. Reading it
// also records the account and its latest credentials.
func currentLogin(agent string) *Account {
	for _, p := range Accounts() {
		if p.Account != nil && p.Account.Agent == agent {
			return p.Account
		}
	}
	return nil
}

// accountCandidates lists the accounts the gateway may send a request through
// for a signed-in agent: the current login first, then the other accounts
// that are 同时生效, in the order they were remembered.
func accountCandidates(p Provider) []Provider {
	out := []Provider{p}
	for _, r := range loadAccounts().Accounts {
		if r.Agent != p.Account.Agent || !r.On || sameUser(r.User, p.Account.User) {
			continue
		}
		if sp, ok := storedAccount(r); ok {
			out = append(out, sp)
		}
	}
	return out
}

// storedAccount is a remembered account that is not the agent's current
// login. It is signed with its own credentials.
func storedAccount(r rememberedAccount) (Provider, bool) {
	user := r.User
	switch r.Agent {
	case "claude":
		if _, ok := parseClaudeCredentials(r.Creds); !ok {
			return Provider{}, false
		}
		acct := &Account{Agent: "claude", User: user, Plan: r.Plan}
		acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
			tok, err := storedClaudeToken(ctx, user)
			if err != nil {
				return err
			}
			claudeSign(req, tok, body)
			return nil
		}
		acct.body = claudeBody
		acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
		return Provider{ID: "claude", Name: "Claude Code", Icon: "claudecode-color", Anthropic: claudeBase,
			Catalog: "anthropic", Website: "https://claude.ai", Account: acct}, true
	case "codex":
		var a codexAuth
		if json.Unmarshal(r.Creds, &a) != nil || a.Tokens.AccessToken == "" {
			return Provider{}, false
		}
		acct := &Account{Agent: "codex", Stream: true, User: user, Plan: r.Plan}
		acct.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
			tok, accountID, err := storedCodexToken(ctx, user)
			if err != nil {
				return err
			}
			codexSign(req, tok, accountID)
			return nil
		}
		acct.body = codexBody
		acct.models = catalog.Codex
		acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
		return Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Responses: CodexBase,
			Website: "https://chatgpt.com/codex", Account: acct}, true
	}
	return Provider{}, false
}

// storedClaudeToken returns a usable access token for a remembered Claude
// account, refreshing its own copy when needed.
func storedClaudeToken(ctx context.Context, user string) (string, error) {
	recordMu.Lock()
	defer recordMu.Unlock()
	r, ok := findRemembered("claude", user)
	c, parsed := parseClaudeCredentials(r.Creds)
	if !ok || !parsed {
		return "", fmt.Errorf("no saved Claude Code sign-in for %s; run claude auth login as that account", user)
	}
	if claudeFresh(c) {
		return c.OAuth.AccessToken, nil
	}
	c, err := claudeRefresh(ctx, c)
	if err != nil {
		return "", err
	}
	blob, err := c.marshal()
	if err != nil {
		return "", err
	}
	if err := setRememberedCreds("claude", user, blob); err != nil {
		return "", err
	}
	return c.OAuth.AccessToken, nil
}

// storedCodexToken returns a usable access token and account id for a
// remembered Codex account, refreshing its own copy when needed.
func storedCodexToken(ctx context.Context, user string) (string, string, error) {
	recordMu.Lock()
	defer recordMu.Unlock()
	r, ok := findRemembered("codex", user)
	var a codexAuth
	if !ok || json.Unmarshal(r.Creds, &a) != nil || a.Tokens.AccessToken == "" {
		return "", "", fmt.Errorf("no saved Codex sign-in for %s; run codex login as that account", user)
	}
	accountID := a.Tokens.AccountID
	if accountID == "" {
		accountID = claimString(jwtClaims(a.Tokens.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if codexFresh(a.Tokens.AccessToken) {
		return a.Tokens.AccessToken, accountID, nil
	}
	idTok, accessTok, refreshTok, err := codexRefreshTokens(ctx, a.Tokens.RefreshToken)
	if err != nil {
		return "", "", err
	}
	var raw map[string]any
	if err := json.Unmarshal(r.Creds, &raw); err != nil || raw == nil {
		return "", "", fmt.Errorf("saved Codex sign-in for %s is unreadable", user)
	}
	setCodexTokens(raw, idTok, accessTok, refreshTok)
	out, err := json.Marshal(raw)
	if err != nil {
		return "", "", err
	}
	if err := setRememberedCreds("codex", user, out); err != nil {
		return "", "", err
	}
	return accessTok, accountID, nil
}

// SetLoginOn makes a remembered account 同时生效 (served alongside the current
// login) or stops it. The current login is always on, so turning it off is
// refused.
func SetLoginOn(agent, user string, on bool) error {
	agent, err := accountAgent(agent)
	if err != nil {
		return err
	}
	if _, ok := findRemembered(agent, user); !ok {
		return fmt.Errorf("no remembered %s account %q", agentLabel(agent), user)
	}
	if cur := currentLogin(agent); cur != nil && sameUser(cur.User, user) && !on {
		return fmt.Errorf("%s is the current %s login, which is always on", cur.User, agentLabel(agent))
	}
	return editAccounts(func(f *accountsFile) bool {
		for i := range f.Accounts {
			if f.Accounts[i].Agent == agent && sameUser(f.Accounts[i].User, user) {
				if f.Accounts[i].On == on {
					return false
				}
				f.Accounts[i].On = on
				return true
			}
		}
		return false
	})
}

// ForgetAccount drops a remembered account. The current login cannot be
// forgotten; sign out of the agent instead.
func ForgetAccount(agent, user string) error {
	agent, err := accountAgent(agent)
	if err != nil {
		return err
	}
	if cur := currentLogin(agent); cur != nil && sameUser(cur.User, user) {
		return fmt.Errorf("%s is the current %s login; switch to another account before forgetting it", cur.User, agentLabel(agent))
	}
	found := false
	err = editAccounts(func(f *accountsFile) bool {
		keep := f.Accounts[:0]
		for _, r := range f.Accounts {
			if r.Agent == agent && sameUser(r.User, user) {
				found = true
				continue
			}
			keep = append(keep, r)
		}
		f.Accounts = keep
		return found
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no remembered %s account %q", agentLabel(agent), user)
	}
	return nil
}

// SwitchAccount makes user the agent's login. The account it replaces keeps
// its latest credentials and takes on the 同时生效 state the new one had;
// nothing else in the agent's own files changes.
func SwitchAccount(agent, user string) error {
	agent, err := accountAgent(agent)
	if err != nil {
		return err
	}
	target, ok := findRemembered(agent, user)
	if !ok {
		return fmt.Errorf("no remembered %s account %q", agentLabel(agent), user)
	}
	// reading the current login also saves its latest credentials
	cur := currentLogin(agent)
	if cur != nil && sameUser(cur.User, target.User) {
		return nil
	}
	if len(target.Creds) == 0 {
		return fmt.Errorf("no saved %s sign-in for %s; sign in as that account again", agentLabel(agent), target.User)
	}
	if err := writeLogin(agent, target.Creds); err != nil {
		return err
	}
	forgetClaudeCredential()
	forgetClaudeStatus()
	if cur == nil {
		return nil
	}
	return editAccounts(func(f *accountsFile) bool {
		for i := range f.Accounts {
			if f.Accounts[i].Agent == agent && sameUser(f.Accounts[i].User, cur.User) {
				f.Accounts[i].On = target.On
				return true
			}
		}
		return false
	})
}

// writeLogin makes creds the agent's login, touching only the login fields.
func writeLogin(agent string, creds []byte) error {
	if agent == "codex" {
		return writeCodexLogin(creds)
	}
	return writeClaudeLogin(creds)
}

func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func writeClaudeLogin(creds []byte) error {
	target, ok := parseClaudeCredentials(creds)
	if !ok {
		return errors.New("the saved Claude Code sign-in is unreadable")
	}
	cur, loc, ok := readClaudeCredential()
	if !ok {
		loc = claudeCredentialLocation{path: filepath.Join(claudeConfigDir(), ".credentials.json"),
			account: os.Getenv("USER"), keychain: claudeKeychain}
	}
	if cur.raw == nil {
		cur.raw = map[string]any{}
	}
	// only the OAuth block changes; the agent's other fields stay as they are
	cur.raw["claudeAiOauth"] = target.raw["claudeAiOauth"]
	cur.OAuth = target.OAuth
	return saveClaudeCredential(loc, cur)
}

// codexLoginKeys are the auth.json fields that make up a Codex login.
var codexLoginKeys = []string{"auth_mode", "OPENAI_API_KEY", "tokens", "last_refresh"}

func writeCodexLogin(creds []byte) error {
	var target map[string]any
	if json.Unmarshal(creds, &target) != nil || target == nil {
		return errors.New("the saved Codex sign-in is unreadable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".codex", "auth.json")
	raw := map[string]any{}
	readJSON(path, &raw)
	if raw == nil {
		raw = map[string]any{}
	}
	for _, k := range codexLoginKeys {
		if v, ok := target[k]; ok {
			raw[k] = v
		} else {
			delete(raw, k)
		}
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// loginCommand is the agent's own sign-in command, which opens its login
// page. A var so tests can stand in for the browser.
var loginCommand = func(agent string) (*exec.Cmd, error) {
	switch agent {
	case "claude":
		path := claudeExecutable()
		if path == "" {
			return nil, errors.New("claude is not installed")
		}
		return exec.Command(path, "auth", "login"), nil
	case "codex":
		path, err := exec.LookPath("codex")
		if err != nil {
			return nil, errors.New("codex is not installed")
		}
		return exec.Command(path, "login"), nil
	}
	return nil, fmt.Errorf("%w %q", ErrUnknownAgent, agent)
}

// AddAccount signs the agent in through its own login page and remembers
// the account it ends up with. The account it replaces is kept, and takes
// the new account's 同时生效 state.
func AddAccount(agent string) (string, error) {
	agent, err := accountAgent(agent)
	if err != nil {
		return "", err
	}
	forgetClaudeCredential()
	forgetClaudeStatus()
	prev := currentLogin(agent)
	cmd, err := loginCommand(agent)
	if err != nil {
		return "", err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s sign-in did not finish: %v", agentLabel(agent), err)
	}
	forgetClaudeCredential()
	forgetClaudeStatus()
	cur := currentLogin(agent)
	if cur == nil {
		return "", fmt.Errorf("%s sign-in did not finish; no account is signed in", agentLabel(agent))
	}
	if prev != nil && !sameUser(prev.User, cur.User) {
		if now, ok := findRemembered(agent, cur.User); ok {
			editAccounts(func(f *accountsFile) bool {
				for i := range f.Accounts {
					if f.Accounts[i].Agent == agent && sameUser(f.Accounts[i].User, prev.User) {
						f.Accounts[i].On = now.On
						return true
					}
				}
				return false
			})
		}
	}
	return cur.User, nil
}

// AccountInfo is a remembered account as `magpie accounts` lists it.
type AccountInfo struct {
	Agent   string
	User    string
	Plan    string
	Current bool // the agent is signed in as this account
	On      bool // served alongside the current login; the current login always is
}

// RememberedAccounts lists the accounts remembered for agent, the current
// login first.
func RememberedAccounts(agent string) ([]AccountInfo, error) {
	agent, err := accountAgent(agent)
	if err != nil {
		return nil, err
	}
	cur := currentLogin(agent)
	var out []AccountInfo
	seen := false
	for _, r := range loadAccounts().Accounts {
		if r.Agent != agent {
			continue
		}
		info := AccountInfo{Agent: agent, User: r.User, Plan: r.Plan, On: r.On}
		if cur != nil && sameUser(cur.User, r.User) {
			info.Current, info.On, seen = true, true, true
			if cur.Plan != "" {
				info.Plan = cur.Plan
			}
			out = append([]AccountInfo{info}, out...)
			continue
		}
		out = append(out, info)
	}
	if cur != nil && !seen {
		out = append([]AccountInfo{{Agent: agent, User: cur.User, Plan: cur.Plan, Current: true, On: true}}, out...)
	}
	return out, nil
}
