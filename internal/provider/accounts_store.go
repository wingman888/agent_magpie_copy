package provider

// Remembered accounts: the subscriptions magpie has seen signed in to an
// agent, so the user can switch between them, and keep more than one of them
// serving requests at once.
//
// The agent keeps its current sign-in where it always did (~/.codex/auth.json,
// Claude Code's credentials). Every other remembered account keeps its
// sign-in in a file of its own under the magpie config directory, mode 0600.
// A sign-in is held in one place at a time, so a refresh token is never
// rotated under a second holder. An account marked on ("同时生效") serves
// requests from its own file and is never written into the agent's store.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// rememberedAgents are the agents whose sign-ins magpie remembers.
var rememberedAgents = []string{"claude", "codex"}

// credentialKeys are the top-level keys of an agent's own store that hold the
// sign-in. Switching replaces these and keeps every other key as it was.
var credentialKeys = map[string][]string{
	"codex":  {"tokens", "last_refresh", "auth_mode"},
	"claude": {"claudeAiOauth"},
}

// rememberedAccount is one entry of accounts.json.
type rememberedAccount struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	// On is "同时生效": the account serves requests alongside the sign-in.
	On bool `json:"on,omitempty"`
	// Saved is set when a copy of its sign-in is in the accounts directory.
	Saved bool `json:"saved,omitempty"`
}

type accountsFile struct {
	Accounts []rememberedAccount `json:"accounts"`
}

// RememberedAccount describes a remembered account, for display.
type RememberedAccount struct {
	Agent   string
	User    string
	Plan    string
	Current bool // signed in to the agent now
	On      bool // serves requests alongside the sign-in (the current one always does)
	Saved   bool // magpie holds a copy of its sign-in
}

// accountsMu serializes every change to accounts.json and the accounts
// directory.
var accountsMu sync.Mutex

func accountsDir() string   { return filepath.Join(filepath.Dir(Path()), "accounts") }
func accountsIndex() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

// stashPath is where a remembered account's sign-in is kept.
func stashPath(agent, user string) string {
	return filepath.Join(accountsDir(), agent+"-"+KeyRef(strings.ToLower(user))+".json")
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

func claudeCredentialPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

func isRememberedAgent(agent string) bool {
	return agent == "claude" || agent == "codex"
}

func unknownAgent(agent string) error {
	return fmt.Errorf("%q has no remembered accounts; the agents with accounts are claude and codex", agent)
}

func loadAccounts() accountsFile {
	var f accountsFile
	if b, err := os.ReadFile(accountsIndex()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func storeAccounts(f accountsFile) error {
	p := accountsIndex()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
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

// findRemembered finds an account by agent and address, ignoring case.
func findRemembered(list []rememberedAccount, agent, user string) int {
	for i, r := range list {
		if r.Agent == agent && strings.EqualFold(r.User, strings.TrimSpace(user)) {
			return i
		}
	}
	return -1
}

// agentSignIn is the account an agent is signed in to now.
type agentSignIn struct{ user, plan string }

// currentSignIns asks every agent with a remembered account who is signed in.
func currentSignIns() map[string]agentSignIn {
	out := map[string]agentSignIn{}
	for _, p := range Accounts() {
		if p.Account != nil {
			out[p.Account.Agent] = agentSignIn{user: p.Account.User, plan: p.Account.Plan}
		}
	}
	return out
}

// agentLogin reads the raw sign-in the agent keeps for itself.
func agentLogin(agent string) (map[string]any, bool) {
	switch agent {
	case "codex":
		var m map[string]any
		if !readJSON(codexAuthPath(), &m) || m == nil {
			return nil, false
		}
		return m, true
	case "claude":
		c, _, ok := readClaudeCredential()
		return c.raw, ok && c.raw != nil
	}
	return nil, false
}

// writeAgentLogin replaces the sign-in in the agent's own store. The store's
// other contents are the caller's to keep: login must already carry them.
func writeAgentLogin(agent string, login map[string]any) error {
	switch agent {
	case "codex":
		path := codexAuthPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		b, err := json.MarshalIndent(login, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
			return err
		}
		return os.Chmod(path, 0o600)
	case "claude":
		c := claudeCredentials{raw: login}
		if ob, err := json.Marshal(login["claudeAiOauth"]); err == nil {
			json.Unmarshal(ob, &c.OAuth)
		}
		_, loc, ok := readClaudeCredential()
		if !ok {
			loc = claudeCredentialLocation{path: claudeCredentialPath()}
			if err := os.MkdirAll(filepath.Dir(loc.path), 0o700); err != nil {
				return err
			}
		}
		return saveClaudeCredential(loc, c)
	}
	return unknownAgent(agent)
}

// mergeLogin is cur with the credential keys taken from next.
func mergeLogin(agent string, cur, next map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range cur {
		out[k] = v
	}
	for _, k := range credentialKeys[agent] {
		if v, ok := next[k]; ok {
			out[k] = v
		} else {
			delete(out, k)
		}
	}
	return out
}

// writeStash saves a copy of an account's sign-in, and reports whether the
// file changed.
func writeStash(agent, user string, login map[string]any) (bool, error) {
	b, err := json.Marshal(login)
	if err != nil {
		return false, err
	}
	p := stashPath(agent, user)
	if old, err := os.ReadFile(p); err == nil && bytes.Equal(old, b) {
		return false, nil
	}
	if err := os.MkdirAll(accountsDir(), 0o700); err != nil {
		return false, err
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return false, err
	}
	return true, os.Chmod(p, 0o600)
}

func readStash(agent, user string) (map[string]any, error) {
	b, err := os.ReadFile(stashPath(agent, user))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("the saved sign-in is empty")
	}
	return m, nil
}

// rememberLoginsLocked records every agent's current sign-in: a new account
// gets an entry. The signed-in account's refresh token lives only in the
// agent's own store, so no copy is kept for it; a stale copy left from before
// it was signed in again is removed. A copy is saved when the account is
// switched away from (see SwitchAccount). The caller holds accountsMu.
func rememberLoginsLocked() error {
	f := loadAccounts()
	dirty := false
	now := currentSignIns()
	for _, agent := range rememberedAgents {
		cur, ok := now[agent]
		if !ok {
			continue
		}
		i := findRemembered(f.Accounts, agent, cur.user)
		if i < 0 {
			f.Accounts = append(f.Accounts, rememberedAccount{Agent: agent, User: cur.user, Plan: cur.plan})
			i = len(f.Accounts) - 1
			dirty = true
		} else if cur.plan != "" && f.Accounts[i].Plan != cur.plan {
			f.Accounts[i].Plan = cur.plan
			dirty = true
		}
		if f.Accounts[i].Saved {
			os.Remove(stashPath(agent, cur.user))
			f.Accounts[i].Saved = false
			dirty = true
		}
	}
	if !dirty {
		return nil
	}
	return storeAccounts(f)
}

// lockedAccounts runs fn holding accountsMu.
func lockedAccounts(fn func() error) error {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	return fn()
}

// observeEvery is how often provider listings look for new sign-ins.
const observeEvery = 30 * time.Second

var observeClock struct {
	sync.Mutex
	at time.Time
}

// observeSignIns remembers the current sign-ins, at most once per observeEvery
// unless force is set.
func observeSignIns(force bool) error {
	observeClock.Lock()
	if !force && time.Since(observeClock.at) < observeEvery {
		observeClock.Unlock()
		return nil
	}
	observeClock.at = time.Now()
	observeClock.Unlock()
	return lockedAccounts(rememberLoginsLocked)
}

// Remembered lists the remembered accounts of agent ("" for every agent),
// after recording any sign-in that is new.
func Remembered(agent string) []RememberedAccount {
	var out []RememberedAccount
	lockedAccounts(func() error {
		_ = rememberLoginsLocked() // a failed save leaves the list as it was
		f := loadAccounts()
		now := currentSignIns()
		for _, r := range f.Accounts {
			if agent != "" && r.Agent != agent {
				continue
			}
			cur, ok := now[r.Agent]
			isCur := ok && strings.EqualFold(cur.user, r.User)
			plan := r.Plan
			if isCur && cur.plan != "" {
				plan = cur.plan
			}
			out = append(out, RememberedAccount{Agent: r.Agent, User: r.User, Plan: plan,
				Current: isCur, On: r.On || isCur, Saved: r.Saved})
		}
		return nil
	})
	return out
}

// SwitchAccount signs the agent in as a remembered account. The sign-in it
// replaces is remembered, and takes over the on state of the account that
// replaces it. It returns the account's address as remembered.
func SwitchAccount(agent, user string) (string, error) {
	if !isRememberedAgent(agent) {
		return "", unknownAgent(agent)
	}
	var name string
	err := lockedAccounts(func() error {
		if err := rememberLoginsLocked(); err != nil {
			return err
		}
		f := loadAccounts()
		i := findRemembered(f.Accounts, agent, user)
		if i < 0 {
			return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agent, strings.TrimSpace(user), agent)
		}
		target := f.Accounts[i]
		name = target.User
		cur, signedIn := currentSignIns()[agent]
		if signedIn && strings.EqualFold(cur.user, target.User) {
			return nil
		}
		if !target.Saved {
			return fmt.Errorf("magpie holds no sign-in for %s; sign in to it again with magpie accounts add %s", target.User, agent)
		}
		next, err := readStash(agent, target.User)
		if err != nil {
			return fmt.Errorf("magpie cannot read the saved sign-in for %s: %v", target.User, err)
		}
		curLogin, curOK := agentLogin(agent)
		// the account signed out of keeps its newest sign-in, saved before the
		// agent's own copy is replaced
		if signedIn && curOK {
			if _, err := writeStash(agent, cur.user, curLogin); err != nil {
				return err
			}
		}
		if err := writeAgentLogin(agent, mergeLogin(agent, curLogin, next)); err != nil {
			return err
		}
		// the account signed out of takes over the on state the target had
		if signedIn {
			if j := findRemembered(f.Accounts, agent, cur.user); j >= 0 {
				f.Accounts[j].On = target.On
				f.Accounts[j].Saved = curOK
			}
		}
		// the target's sign-in is now the agent's own alone
		os.Remove(stashPath(agent, target.User))
		f.Accounts[i].Saved = false
		forgetClaudeCredential()
		forgetClaudeStatus()
		return storeAccounts(f)
	})
	return name, err
}

// ForgetAccount drops a remembered account and its saved sign-in. The account
// the agent is signed in to cannot be forgotten.
func ForgetAccount(agent, user string) error {
	if !isRememberedAgent(agent) {
		return unknownAgent(agent)
	}
	return lockedAccounts(func() error {
		if err := rememberLoginsLocked(); err != nil {
			return err
		}
		f := loadAccounts()
		i := findRemembered(f.Accounts, agent, user)
		if i < 0 {
			return fmt.Errorf("no remembered %s account %q", agent, strings.TrimSpace(user))
		}
		if cur, ok := currentSignIns()[agent]; ok && strings.EqualFold(cur.user, f.Accounts[i].User) {
			return fmt.Errorf("%s is the account %s is signed in to; sign it out before forgetting it", f.Accounts[i].User, agentName(agent))
		}
		os.Remove(stashPath(agent, f.Accounts[i].User))
		f.Accounts = append(f.Accounts[:i], f.Accounts[i+1:]...)
		return storeAccounts(f)
	})
}

// SetLoginOn lets a remembered account serve requests at the same time as the
// agent's sign-in ("同时生效"), or stops it. The signed-in account always
// serves requests, so turning it off is refused.
func SetLoginOn(agent, user string, on bool) error {
	if !isRememberedAgent(agent) {
		return unknownAgent(agent)
	}
	return lockedAccounts(func() error {
		if err := rememberLoginsLocked(); err != nil {
			return err
		}
		f := loadAccounts()
		i := findRemembered(f.Accounts, agent, user)
		if i < 0 {
			return fmt.Errorf("no remembered %s account %q", agent, strings.TrimSpace(user))
		}
		if cur, ok := currentSignIns()[agent]; ok && strings.EqualFold(cur.user, f.Accounts[i].User) {
			if !on {
				return fmt.Errorf("%s is signed in to %s now, and always serves requests", f.Accounts[i].User, agentName(agent))
			}
			return nil
		}
		f.Accounts[i].On = on
		return storeAccounts(f)
	})
}

// AddAccount runs the agent's own sign-in, which opens the vendor's sign-in
// page and returns once the user has finished, then remembers the account
// that is signed in. A new account joins alongside the ones already
// remembered: when there are others, it serves requests at the same time.
func AddAccount(agent string) (string, error) {
	if !isRememberedAgent(agent) {
		return "", unknownAgent(agent)
	}
	exe, args, err := signInCommand(agent)
	if err != nil {
		return "", err
	}
	var before []rememberedAccount
	if err := lockedAccounts(func() error {
		if err := rememberLoginsLocked(); err != nil {
			return err
		}
		before = loadAccounts().Accounts
		return nil
	}); err != nil {
		return "", err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s sign-in did not finish: %v", agentName(agent), err)
	}
	var user string
	err = lockedAccounts(func() error {
		if err := rememberLoginsLocked(); err != nil {
			return err
		}
		cur, ok := currentSignIns()[agent]
		if !ok {
			return fmt.Errorf("%s is still signed out", agentName(agent))
		}
		user = cur.user
		f := loadAccounts()
		i := findRemembered(f.Accounts, agent, cur.user)
		if i < 0 {
			return fmt.Errorf("magpie could not remember %s", cur.user)
		}
		isNew := findRemembered(before, agent, cur.user) < 0
		others := 0
		for j, r := range f.Accounts {
			if j != i && r.Agent == agent {
				others++
			}
		}
		if isNew && others > 0 {
			f.Accounts[i].On = true
		}
		return storeAccounts(f)
	})
	return user, err
}

func signInCommand(agent string) (string, []string, error) {
	switch agent {
	case "claude":
		exe := claudeExecutable()
		if exe == "" {
			return "", nil, errors.New("magpie cannot find the claude command; install Claude Code first")
		}
		return exe, []string{"auth", "login"}, nil
	case "codex":
		exe, err := exec.LookPath("codex")
		if err != nil {
			return "", nil, errors.New("magpie cannot find the codex command; install Codex CLI first")
		}
		return exe, []string{"login"}, nil
	}
	return "", nil, unknownAgent(agent)
}

// agentName is the agent's name as the user knows it.
func agentName(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	}
	return agent
}

// onAccounts are the remembered accounts of agent that serve requests
// alongside its sign-in, in the order they were saved, as providers that sign
// with their own sign-in.
func onAccounts(agent, current string) []Provider {
	var out []Provider
	for _, r := range loadAccounts().Accounts {
		if r.Agent != agent || !r.On || !r.Saved || strings.EqualFold(r.User, current) {
			continue
		}
		if agent == "codex" {
			out = append(out, codexProvider(stashPath(agent, r.User), r.User, r.Plan))
		}
	}
	return out
}

// accountPlans lists the sign-ins that may serve a request through a signed-in
// account: the agent's current sign-in first, then the accounts that serve
// alongside it, in the order they were saved.
func (p Provider) accountPlans() []KeyPlan {
	ps := append([]Provider{p}, onAccounts(p.Account.Agent, p.Account.User)...)
	var out []KeyPlan
	for _, a := range ps {
		ups := a.Speaks()
		if len(ups) == 0 {
			continue
		}
		out = append(out, KeyPlan{Provider: a, Ref: KeyRef("account " + strings.ToLower(a.Account.User)),
			Name: a.Account.User, Upstreams: ups})
	}
	return out
}

// usageEntry is one agent's last LoginUsage result.
type usageEntry struct {
	at   time.Time
	byID map[string]SubscriptionQuota
}

var loginUsageCache struct {
	sync.Mutex
	by map[string]*usageEntry
}

// LoginUsage reports the rolling usage of each remembered account of agent,
// keyed by its address: the 5 hour and weekly windows, as fractions used. A
// result is reused for a minute. An account whose query fails keeps what was
// last known about it. An agent without accounts gets an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	if !isRememberedAgent(agent) {
		return out
	}
	c := &loginUsageCache
	c.Lock()
	defer c.Unlock()
	if c.by == nil {
		c.by = map[string]*usageEntry{}
	}
	e := c.by[agent]
	if e == nil {
		e = &usageEntry{byID: map[string]SubscriptionQuota{}}
		c.by[agent] = e
	}
	if time.Since(e.at) < time.Minute {
		return copyQuotas(e.byID)
	}
	fresh := map[string]SubscriptionQuota{}
	for _, r := range Remembered(agent) {
		var q SubscriptionQuota
		switch {
		case agent == "codex" && r.Current:
			q = timedQuota(ctx, func(ctx context.Context) SubscriptionQuota { return codexSubscriptionUsage(ctx, codexAuthPath()) })
		case agent == "codex" && r.Saved:
			q = timedQuota(ctx, func(ctx context.Context) SubscriptionQuota {
				return codexSubscriptionUsage(ctx, stashPath(agent, r.User))
			})
		case agent == "claude" && r.Current:
			q = timedQuota(ctx, claudeSubscriptionUsage)
		default:
			continue
		}
		if prev, ok := e.byID[r.User]; ok && q.Error != "" && prev.Error == "" {
			fresh[r.User] = prev
			continue
		}
		if q.Plan == "" {
			q.Plan = r.Plan
		}
		fresh[r.User] = q
	}
	e.at, e.byID = time.Now(), fresh
	return copyQuotas(fresh)
}

// timedQuota runs one account's usage query under subscriptionTimeout.
func timedQuota(ctx context.Context, fetch func(context.Context) SubscriptionQuota) SubscriptionQuota {
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	return fetch(ctx)
}

func copyQuotas(m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
