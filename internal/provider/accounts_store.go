package provider

// Saved sign-ins: the Claude Code and Codex accounts magpie remembers.
//
// An agent keeps one login at a time. magpie keeps a copy of every account it
// has seen, under <config>/magpie/accounts/<agent>/: the directory is 0700,
// each credential file 0600, and accounts.json indexes them (who, plan, the
// order seen, and whether the account is on at the same time as the signed-in
// one).
//
// Refresh tokens have one holder at a time. The account the agent is signed in
// to is refreshed by the agent alone; magpie's copy of it only follows the
// agent's file. A remembered account that is not signed in is refreshed by
// magpie alone, so no two writers ever rotate the same token.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// accountsMu serializes reads and writes of the saved sign-ins.
var accountsMu sync.Mutex

// savedAccount is one remembered sign-in in accounts.json.
type savedAccount struct {
	User  string    `json:"user"`
	Plan  string    `json:"plan,omitempty"`
	On    bool      `json:"on,omitempty"` // on at the same time as the signed-in account
	File  string    `json:"file"`         // the credential file, in the agent's directory
	Saved time.Time `json:"saved"`
}

// savedIndex is accounts.json; the order of Accounts is the order seen.
type savedIndex struct {
	Accounts []savedAccount `json:"accounts"`
}

func (x savedIndex) find(user string) int {
	for i, a := range x.Accounts {
		if strings.EqualFold(a.User, user) {
			return i
		}
	}
	return -1
}

// SavedAccount is a remembered sign-in, as listed to the user.
type SavedAccount struct {
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Plan    string `json:"plan,omitempty"`
	Current bool   `json:"current"` // the account the agent is signed in to
	On      bool   `json:"on"`      // on at the same time (the signed-in one always is)
}

// checkSavedAgent accepts the two agents that have saved sign-ins.
func checkSavedAgent(agent string) error {
	switch agent {
	case "claude", "codex":
		return nil
	}
	return fmt.Errorf("%q has no saved sign-ins; claude and codex do", agent)
}

func storeDir(agent string) string {
	return filepath.Join(filepath.Dir(Path()), "accounts", agent)
}

func loadIndex(agent string) savedIndex {
	var x savedIndex
	readJSON(filepath.Join(storeDir(agent), "accounts.json"), &x)
	return x
}

func saveIndex(agent string, x savedIndex) error {
	b, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(filepath.Join(storeDir(agent), "accounts.json"), append(b, '\n'))
}

// writeSecret writes a file only its owner can read, creating its directory
// as the owner's alone.
func writeSecret(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// credFileName names a saved credential after the account, without the address.
func credFileName(user string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(user)))
	return hex.EncodeToString(sum[:8]) + ".json"
}

func credPath(agent string, a savedAccount) string {
	return filepath.Join(storeDir(agent), a.File)
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

func claudeCredentialsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// signedIn reads the account an agent is signed in to: who it is, its plan,
// and its credential exactly as the agent keeps it.
func signedIn(agent string) (string, string, []byte, bool) {
	switch agent {
	case "codex":
		b, err := os.ReadFile(codexAuthPath())
		var a codexAuth
		if err != nil || json.Unmarshal(b, &a) != nil || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return "", "", nil, false
		}
		id := jwtClaims(a.Tokens.IDToken)
		user := claimString(id, "email")
		if user == "" {
			user = "ChatGPT"
		}
		return user, claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"), b, true
	case "claude":
		c, _, ok := claudeCredential()
		if !ok {
			return "", "", nil, false
		}
		user, plan, out := claudeWho(c)
		if out {
			return "", "", nil, false
		}
		b, err := c.marshal()
		if err != nil {
			return "", "", nil, false
		}
		return user, plan, b, true
	}
	return "", "", nil, false
}

// remember records a signed-in account and the credential it holds now. The
// second account seen is on at the same time by default. Caller holds accountsMu.
func remember(agent, user, plan string, cred []byte) error {
	x := loadIndex(agent)
	i := x.find(user)
	changed := false
	if i < 0 {
		x.Accounts = append(x.Accounts, savedAccount{User: user, On: len(x.Accounts) > 0,
			File: credFileName(user), Saved: time.Now()})
		i = len(x.Accounts) - 1
		changed = true
	}
	a := &x.Accounts[i]
	if a.File == "" {
		a.File = credFileName(a.User)
		changed = true
	}
	if plan != "" && a.Plan != plan {
		a.Plan = plan
		changed = true
	}
	path := credPath(agent, *a)
	if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, cred) {
		if err := writeSecret(path, cred); err != nil {
			return err
		}
	}
	if changed {
		return saveIndex(agent, x)
	}
	return nil
}

// observe remembers the account an agent is signed in to, if it is signed in.
// Caller holds accountsMu.
func observe(agent string) error {
	user, plan, cred, ok := signedIn(agent)
	if !ok {
		return nil
	}
	return remember(agent, user, plan, cred)
}

// rememberSignIns keeps the saved sign-ins current. Providers are listed often,
// so nothing is written unless a sign-in changed.
func rememberSignIns() {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	for _, agent := range []string{"claude", "codex"} {
		_ = observe(agent)
	}
}

// RememberSignIn records the account an agent is signed in to.
func RememberSignIn(agent string) error {
	if err := checkSavedAgent(agent); err != nil {
		return err
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	return observe(agent)
}

// ForgetSignIn drops what magpie has cached of the agents' sign-ins, so the
// next look reads them afresh (after a login has just finished).
func ForgetSignIn() {
	forgetClaudeCredential()
	forgetClaudeStatus()
}

// SavedAccounts lists the accounts remembered for an agent, the signed-in one
// included, in the order they were first seen.
func SavedAccounts(agent string) ([]SavedAccount, error) {
	if err := checkSavedAgent(agent); err != nil {
		return nil, err
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if err := observe(agent); err != nil {
		return nil, err
	}
	cur, _, _, hasCur := signedIn(agent)
	var out []SavedAccount
	for _, a := range loadIndex(agent).Accounts {
		now := hasCur && strings.EqualFold(cur, a.User)
		out = append(out, SavedAccount{Agent: agent, User: a.User, Plan: a.Plan, Current: now, On: a.On || now})
	}
	return out, nil
}

// writeLogin puts a remembered credential where the agent keeps its login.
func writeLogin(agent string, cred []byte) error {
	switch agent {
	case "codex":
		return writeCodexLogin(codexAuthPath(), cred)
	case "claude":
		return writeClaudeLogin(cred)
	}
	return checkSavedAgent(agent)
}

// writeCodexLogin replaces the login fields of the Codex auth.json. Every
// other field the file holds is kept as Codex wrote it.
func writeCodexLogin(path string, cred []byte) error {
	var next map[string]any
	if json.Unmarshal(cred, &next) != nil {
		return errors.New("the saved Codex login is unreadable")
	}
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		var old map[string]any
		if json.Unmarshal(b, &old) == nil && old != nil {
			raw = old
		}
	}
	for _, k := range []string{"auth_mode", "tokens", "last_refresh"} {
		if v, ok := next[k]; ok {
			raw[k] = v
		} else {
			delete(raw, k)
		}
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(path, append(out, '\n'))
}

// writeClaudeLogin replaces the OAuth login in Claude Code's credentials. The
// MCP OAuth state and anything else in the file is kept.
func writeClaudeLogin(cred []byte) error {
	next, ok := parseClaudeCredentials(cred)
	if !ok {
		return errors.New("the saved Claude Code login is unreadable")
	}
	var nextRaw map[string]any
	json.Unmarshal(cred, &nextRaw)
	c, loc, ok := readClaudeCredential()
	if !ok {
		c = claudeCredentials{raw: map[string]any{}}
		loc = claudeCredentialLocation{path: claudeCredentialsPath()}
		if claudeKeychain {
			loc = claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
		}
	}
	if c.raw == nil {
		c.raw = map[string]any{}
	}
	c.raw["claudeAiOauth"] = nextRaw["claudeAiOauth"]
	c.OAuth = next.OAuth
	return saveClaudeCredential(loc, c)
}

// SwitchAccount signs an agent in to a remembered account. The account it
// replaces is kept with its latest credential and hands its "on at the same
// time" state to the one it gives way to. Only the login changes; the rest of
// the agent's config is left as it is.
func SwitchAccount(agent, user string) error {
	if err := checkSavedAgent(agent); err != nil {
		return err
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if err := observe(agent); err != nil {
		return err
	}
	x := loadIndex(agent)
	t := x.find(user)
	if t < 0 {
		return fmt.Errorf("no remembered %s account %s; magpie accounts %s lists them", agent, user, agent)
	}
	target := x.Accounts[t]
	cur, _, _, hasCur := signedIn(agent)
	if hasCur && strings.EqualFold(cur, target.User) {
		return nil
	}
	cred, err := os.ReadFile(credPath(agent, target))
	if err != nil {
		return err
	}
	if err := writeLogin(agent, cred); err != nil {
		return err
	}
	if hasCur {
		if o := x.find(cur); o >= 0 {
			x.Accounts[o].On = target.On
		}
	}
	ForgetSignIn()
	return saveIndex(agent, x)
}

// ForgetAccount drops a remembered account. The one signed in is refused: the
// agent's own sign-out or a switch is the way to let go of it.
func ForgetAccount(agent, user string) error {
	if err := checkSavedAgent(agent); err != nil {
		return err
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if err := observe(agent); err != nil {
		return err
	}
	cur, _, _, hasCur := signedIn(agent)
	if hasCur && strings.EqualFold(cur, user) {
		return fmt.Errorf("%s is the account signed in to %s; switch to another account first", user, agent)
	}
	x := loadIndex(agent)
	i := x.find(user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %s", agent, user)
	}
	if f := x.Accounts[i].File; f != "" {
		os.Remove(credPath(agent, x.Accounts[i]))
	}
	x.Accounts = append(x.Accounts[:i], x.Accounts[i+1:]...)
	return saveIndex(agent, x)
}

// SetLoginOn keeps a remembered account on at the same time as the signed-in
// one, or takes that back. The signed-in account is always on, so turning it
// off is refused. The agent's own credential store is never written.
func SetLoginOn(agent, user string, on bool) error {
	if err := checkSavedAgent(agent); err != nil {
		return err
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if err := observe(agent); err != nil {
		return err
	}
	cur, _, _, hasCur := signedIn(agent)
	x := loadIndex(agent)
	i := x.find(user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %s", agent, user)
	}
	if hasCur && strings.EqualFold(cur, x.Accounts[i].User) {
		if on {
			return nil
		}
		return fmt.Errorf("%s is signed in to %s and is always on", x.Accounts[i].User, agent)
	}
	x.Accounts[i].On = on
	return saveIndex(agent, x)
}

// SignedInProviders lists the ways to reach a provider for a request: the
// account signed in to the agent first, then each remembered account that is
// on at the same time, in the order they were remembered. Each carries its own
// login. Only Codex accounts are served this way; any other provider is just
// itself.
func SignedInProviders(p Provider) []Provider {
	out := []Provider{p}
	if p.Account == nil || p.Account.Agent != "codex" {
		return out
	}
	accountsMu.Lock()
	x := loadIndex("codex")
	accountsMu.Unlock()
	for _, a := range x.Accounts {
		if !a.On || strings.EqualFold(a.User, p.Account.User) {
			continue
		}
		q := p
		q.Account = newCodexAccount(credPath("codex", a), a.User, a.Plan)
		out = append(out, q)
	}
	return out
}

// storedClaudeToken returns the access token of a remembered Claude account,
// refreshing it in its own file when it is about to expire.
func storedClaudeToken(path string) func(context.Context) (string, error) {
	load := func() (claudeCredentials, claudeCredentialLocation, bool) {
		b, err := os.ReadFile(path)
		if err != nil {
			return claudeCredentials{}, claudeCredentialLocation{}, false
		}
		c, ok := parseClaudeCredentials(b)
		return c, claudeCredentialLocation{path: path}, ok
	}
	return func(ctx context.Context) (string, error) {
		tok, err := claudeTokenFrom(ctx, load, load)
		// the refresh was written to this account's file, not the signed-in one's
		forgetClaudeCredential()
		return tok, err
	}
}
