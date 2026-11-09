package provider

// Remembered accounts: the subscription logins magpie has seen, so the user can
// switch between them and keep several signed in at once.
//
// One refresh token has one holder. The account the agent is signed in to keeps
// its tokens in the agent's own store, and magpie keeps no copy of them. Every
// other remembered account keeps its tokens in a 0600 file under magpie's
// config directory, which is then the only copy. Switching moves tokens between
// the two. accounts.json names the accounts and says which ones answer
// requests; it holds no secrets.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// RememberedAccount is a subscription account magpie knows about.
type RememberedAccount struct {
	Agent  string
	User   string // the account's email, as the agent shows it
	Plan   string
	Active bool // signed in to the agent right now
	On     bool // answers requests beside the agent's own sign-in; the active one always does
}

// rememberedRecord is what accounts.json keeps about one account.
type rememberedRecord struct {
	Agent string    `json:"agent"`
	User  string    `json:"user"`
	Plan  string    `json:"plan,omitempty"`
	On    bool      `json:"on,omitempty"`
	Saved time.Time `json:"saved"`
}

type accountIndex struct {
	Accounts []rememberedRecord `json:"accounts"`
}

// acctStoreMu serializes changes to the index and to the token files.
var acctStoreMu sync.Mutex

// codexLoginKeys are the keys of Codex's auth file that hold the sign-in. The
// rest of the file belongs to Codex CLI and is left alone.
var codexLoginKeys = []string{"auth_mode", "tokens", "last_refresh"}

// LoginRunner runs the agent's own sign-in, which opens the vendor's login page
// and returns once the user has signed in. A var so tests can stand in for it.
var LoginRunner = func(agent string) error {
	var cmd *exec.Cmd
	switch agent {
	case "claude":
		exe := claudeExecutable()
		if exe == "" {
			return errors.New("Claude Code is not installed")
		}
		cmd = exec.Command(exe, "auth", "login")
	case "codex":
		exe, err := exec.LookPath("codex")
		if err != nil {
			return errors.New("Codex CLI is not installed")
		}
		cmd = exec.Command(exe, "login")
	default:
		return unknownAgentError(agent)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// AccountAgent maps a name the user typed to the agent accounts work with:
// claude (or cc) and codex.
func AccountAgent(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func unknownAgentError(name string) error {
	return fmt.Errorf("unknown agent %q: accounts work with claude (cc) and codex", name)
}

func agentTitle(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// accountsIndexPath is the list of remembered accounts.
func accountsIndexPath() string {
	return filepath.Join(filepath.Dir(Path()), "accounts.json")
}

// secretPath is where magpie keeps the sign-in of a remembered account the
// agent is not signed in to.
func secretPath(agent, user string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + strings.ToLower(user)))
	return filepath.Join(filepath.Dir(Path()), "accounts", agent+"-"+hex.EncodeToString(sum[:8])+".json")
}

// writeSecretFile writes data readable by the user alone.
func writeSecretFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func writeSecretBlob(path string, blob map[string]any) error {
	b, err := json.MarshalIndent(blob, "", "  ")
	if err != nil {
		return err
	}
	return writeSecretFile(path, append(b, '\n'))
}

func readSecretBlob(path string) (map[string]any, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var blob map[string]any
	if json.Unmarshal(b, &blob) != nil || len(blob) == 0 {
		return nil, false
	}
	return blob, true
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func loadAccountIndex() accountIndex {
	var idx accountIndex
	if b, err := os.ReadFile(accountsIndexPath()); err == nil {
		json.Unmarshal(b, &idx)
	}
	return idx
}

func saveAccountIndex(idx accountIndex) error {
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return writeSecretFile(accountsIndexPath(), append(b, '\n'))
}

func (idx *accountIndex) find(agent, user string) int {
	for i, r := range idx.Accounts {
		if r.Agent == agent && strings.EqualFold(r.User, user) {
			return i
		}
	}
	return -1
}

func (idx *accountIndex) count(agent string) int {
	n := 0
	for _, r := range idx.Accounts {
		if r.Agent == agent {
			n++
		}
	}
	return n
}

// claudeConfigDir is where Claude Code keeps its files.
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// liveIdentity says which account the agent is signed in to right now.
func liveIdentity(agent string) (string, string, bool) {
	switch agent {
	case "claude":
		user, plan, _, ok := claudeLiveIdentity()
		return user, plan, ok
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", false
		}
		p, ok := codexAccount(home)
		if !ok {
			return "", "", false
		}
		return p.Account.User, p.Account.Plan, true
	}
	return "", "", false
}

// liveBlob is the sign-in the agent keeps itself, reduced to the keys magpie moves.
func liveBlob(agent string) (map[string]any, bool) {
	switch agent {
	case "claude":
		c, _, ok := readClaudeCredential()
		oauth, isMap := c.raw["claudeAiOauth"].(map[string]any)
		if !ok || !isMap {
			return nil, false
		}
		return map[string]any{"claudeAiOauth": oauth}, true
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, false
		}
		var raw map[string]any
		if !readJSON(codexAuthPath(home), &raw) {
			return nil, false
		}
		blob := map[string]any{}
		for _, k := range codexLoginKeys {
			if v, ok := raw[k]; ok {
				blob[k] = v
			}
		}
		return blob, len(blob) > 0
	}
	return nil, false
}

// writeLiveBlob makes blob the sign-in the agent uses. The rest of the agent's
// file is kept as it is.
func writeLiveBlob(agent string, blob map[string]any) error {
	switch agent {
	case "claude":
		return writeLiveClaude(blob)
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path := codexAuthPath(home)
		raw := map[string]any{}
		if b, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(b, &raw) != nil {
				return errors.New("Codex auth file is unreadable; not changing it")
			}
		}
		if raw == nil {
			raw = map[string]any{}
		}
		for _, k := range codexLoginKeys {
			if v, ok := blob[k]; ok {
				raw[k] = v
			} else {
				delete(raw, k)
			}
		}
		b, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		return writeSecretFile(path, append(b, '\n'))
	}
	return unknownAgentError(agent)
}

func writeLiveClaude(blob map[string]any) error {
	oauth, ok := blob["claudeAiOauth"].(map[string]any)
	if !ok {
		return errors.New("saved Claude sign-in is incomplete")
	}
	cur, loc, ok := readClaudeCredential()
	if !ok {
		// nothing usable there yet: start the file the way Claude Code would
		loc = claudeCredentialLocation{path: filepath.Join(claudeConfigDir(), ".credentials.json")}
		cur = claudeCredentials{}
		if b, err := os.ReadFile(loc.path); err == nil {
			if json.Unmarshal(b, &cur.raw) != nil {
				return errors.New("Claude Code credentials file is unreadable; not changing it")
			}
		}
		if cur.raw == nil {
			cur.raw = map[string]any{}
		}
	}
	ob, err := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err != nil {
		return err
	}
	next, ok := parseClaudeCredentials(ob)
	if !ok {
		return errors.New("saved Claude sign-in has no access token")
	}
	// everything else in the file (MCP OAuth state, for one) stays as it was
	next.raw = cur.raw
	next.raw["claudeAiOauth"] = oauth
	return saveClaudeCredential(loc, next)
}

// forgetLiveCaches drops what was read about the agent's sign-in before it changed.
func forgetLiveCaches() {
	forgetClaudeCredential()
	forgetClaudeStatus()
}

// rememberLive records the account the agent is signed in to. The agent keeps
// that account's tokens itself, so any copy magpie had is dropped.
func rememberLive(agent string, idx *accountIndex) {
	user, plan, ok := liveIdentity(agent)
	if !ok {
		return
	}
	if i := idx.find(agent, user); i >= 0 {
		idx.Accounts[i].Plan = plan
	} else {
		idx.Accounts = append(idx.Accounts, rememberedRecord{Agent: agent, User: user, Plan: plan, Saved: time.Now()})
	}
	os.Remove(secretPath(agent, user))
}

// RememberedAccounts lists the accounts remembered for agent, in the order they
// were remembered. The account the agent is signed in to is remembered too.
func RememberedAccounts(agent string) ([]RememberedAccount, error) {
	canon, ok := AccountAgent(agent)
	if !ok {
		return nil, unknownAgentError(agent)
	}
	acctStoreMu.Lock()
	defer acctStoreMu.Unlock()
	idx := loadAccountIndex()
	rememberLive(canon, &idx)
	if err := saveAccountIndex(idx); err != nil {
		return nil, err
	}
	liveUser, _, live := liveIdentity(canon)
	var out []RememberedAccount
	for _, r := range idx.Accounts {
		if r.Agent != canon {
			continue
		}
		isLive := live && strings.EqualFold(r.User, liveUser)
		out = append(out, RememberedAccount{Agent: canon, User: r.User, Plan: r.Plan,
			Active: isLive, On: isLive || (r.On && fileExists(secretPath(canon, r.User)))})
	}
	return out, nil
}

// SwitchAccount signs the agent in to a remembered account. The account it was
// signed in to is kept, with the newest tokens it has, and takes over the on/off
// state of the account it gives way to. Sessions already running keep the old
// account until they restart.
func SwitchAccount(agent, user string) error {
	canon, ok := AccountAgent(agent)
	if !ok {
		return unknownAgentError(agent)
	}
	acctStoreMu.Lock()
	defer acctStoreMu.Unlock()
	idx := loadAccountIndex()
	rememberLive(canon, &idx)
	i := idx.find(canon, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q; see magpie accounts %s", agentTitle(canon), user, canon)
	}
	target := idx.Accounts[i]
	liveUser, _, live := liveIdentity(canon)
	if live && strings.EqualFold(liveUser, target.User) {
		return saveAccountIndex(idx)
	}
	blob, ok := readSecretBlob(secretPath(canon, target.User))
	if !ok {
		return fmt.Errorf("no saved sign-in for %s; sign in to it with magpie accounts add %s", target.User, canon)
	}
	var prev map[string]any
	if live {
		if prev, ok = liveBlob(canon); !ok {
			return fmt.Errorf("could not read the %s sign-in now; nothing switched", agentTitle(canon))
		}
		// keep the account being replaced, with the newest tokens it has
		if err := writeSecretBlob(secretPath(canon, liveUser), prev); err != nil {
			return err
		}
	}
	if err := writeLiveBlob(canon, blob); err != nil {
		if live {
			os.Remove(secretPath(canon, liveUser))
		}
		return err
	}
	forgetLiveCaches()
	// the target's tokens now belong to the agent alone
	os.Remove(secretPath(canon, target.User))
	if live {
		if j := idx.find(canon, liveUser); j >= 0 {
			idx.Accounts[j].On = target.On
		}
	}
	return saveAccountIndex(idx)
}

// ForgetAccount drops a remembered account and its saved sign-in. The account
// the agent is signed in to cannot be forgotten.
func ForgetAccount(agent, user string) error {
	canon, ok := AccountAgent(agent)
	if !ok {
		return unknownAgentError(agent)
	}
	acctStoreMu.Lock()
	defer acctStoreMu.Unlock()
	idx := loadAccountIndex()
	rememberLive(canon, &idx)
	i := idx.find(canon, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", agentTitle(canon), user)
	}
	name := idx.Accounts[i].User
	liveUser, _, live := liveIdentity(canon)
	if live && strings.EqualFold(liveUser, name) {
		return fmt.Errorf("%s is the account %s is signed in to; switch to another account before forgetting it", name, agentTitle(canon))
	}
	os.Remove(secretPath(canon, name))
	idx.Accounts = append(idx.Accounts[:i], idx.Accounts[i+1:]...)
	return saveAccountIndex(idx)
}

// SetLoginOn makes a remembered account answer requests beside the agent's own
// sign-in, or stops it doing so. The account the agent is signed in to always
// answers.
func SetLoginOn(agent, user string, on bool) error {
	canon, ok := AccountAgent(agent)
	if !ok {
		return unknownAgentError(agent)
	}
	acctStoreMu.Lock()
	defer acctStoreMu.Unlock()
	idx := loadAccountIndex()
	rememberLive(canon, &idx)
	i := idx.find(canon, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", agentTitle(canon), user)
	}
	name := idx.Accounts[i].User
	liveUser, _, live := liveIdentity(canon)
	if live && strings.EqualFold(liveUser, name) {
		if !on {
			return fmt.Errorf("%s is signed in to %s and always answers; switch to another account to stop it", name, agentTitle(canon))
		}
		return saveAccountIndex(idx)
	}
	if on && !fileExists(secretPath(canon, name)) {
		return fmt.Errorf("no saved sign-in for %s; sign in to it with magpie accounts add %s", name, canon)
	}
	idx.Accounts[i].On = on
	return saveAccountIndex(idx)
}

// AddAccount opens the agent's own sign-in and remembers the account that comes
// out of it. Once there is more than one account, a new one answers requests
// beside the others. The account it replaces is kept and takes over its state.
func AddAccount(agent string) (RememberedAccount, error) {
	canon, ok := AccountAgent(agent)
	if !ok {
		return RememberedAccount{}, unknownAgentError(agent)
	}
	acctStoreMu.Lock()
	defer acctStoreMu.Unlock()
	idx := loadAccountIndex()
	rememberLive(canon, &idx)
	prevUser, _, hadPrev := liveIdentity(canon)
	var prevBlob map[string]any
	if hadPrev {
		if prevBlob, hadPrev = liveBlob(canon); hadPrev {
			// the sign-in is about to be replaced, so keep it first
			if err := writeSecretBlob(secretPath(canon, prevUser), prevBlob); err != nil {
				return RememberedAccount{}, err
			}
		}
	}
	loginErr := LoginRunner(canon)
	forgetLiveCaches()
	newUser, newPlan, live := liveIdentity(canon)
	if hadPrev && (!live || strings.EqualFold(newUser, prevUser)) {
		// the agent did not end up on a new account, so it keeps the one it had
		if !live {
			if err := writeLiveBlob(canon, prevBlob); err != nil {
				return RememberedAccount{}, err
			}
			forgetLiveCaches()
		}
		os.Remove(secretPath(canon, prevUser))
	}
	if loginErr != nil {
		return RememberedAccount{}, loginErr
	}
	if !live {
		return RememberedAccount{}, fmt.Errorf("the %s sign-in did not finish", agentTitle(canon))
	}
	i := idx.find(canon, newUser)
	if i < 0 {
		// a second account answers beside the first one
		on := idx.count(canon) >= 1
		idx.Accounts = append(idx.Accounts, rememberedRecord{Agent: canon, User: newUser, Plan: newPlan, On: on, Saved: time.Now()})
		i = len(idx.Accounts) - 1
	} else {
		idx.Accounts[i].Plan = newPlan
	}
	os.Remove(secretPath(canon, newUser))
	if hadPrev && !strings.EqualFold(prevUser, newUser) {
		if j := idx.find(canon, prevUser); j >= 0 {
			idx.Accounts[j].On = idx.Accounts[i].On
		}
	}
	if err := saveAccountIndex(idx); err != nil {
		return RememberedAccount{}, err
	}
	rec := idx.Accounts[i]
	return RememberedAccount{Agent: canon, User: rec.User, Plan: rec.Plan, Active: true, On: true}, nil
}

// extraAccounts are the remembered accounts that answer requests beside the
// agent's own sign-in, in the order they were remembered.
func extraAccounts(agent string) []Provider {
	idx := loadAccountIndex()
	liveUser, _, live := liveIdentity(agent)
	var out []Provider
	for _, r := range idx.Accounts {
		if r.Agent != agent || !r.On || (live && strings.EqualFold(r.User, liveUser)) {
			continue
		}
		path := secretPath(agent, r.User)
		if !fileExists(path) {
			continue
		}
		out = append(out, heldProvider(r, path))
	}
	return out
}

// heldProvider is a remembered account that is not the agent's live login. It
// signs in with the tokens magpie keeps at path.
func heldProvider(r rememberedRecord, path string) Provider {
	if r.Agent == "codex" {
		acct := &Account{Agent: "codex", Stream: true, User: r.User, Plan: r.Plan}
		return codexProvider(acct, "codex-"+Slug(r.User), path)
	}
	acct := &Account{Agent: "claude", User: r.User, Plan: r.Plan}
	token := func(ctx context.Context) (string, error) { return claudeHeldToken(ctx, path) }
	return claudeProvider(acct, "claude-"+Slug(r.User), token)
}

// claudeHeldToken is claudeToken for an account magpie keeps itself. It
// refreshes the copy at path, which is the only copy of that sign-in.
func claudeHeldToken(ctx context.Context, path string) (string, error) {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("no saved sign-in for this Claude account; sign in to it with magpie accounts add claude")
	}
	c, ok := parseClaudeCredentials(b)
	if !ok {
		return "", errors.New("saved Claude sign-in is unreadable; sign in to it again with magpie accounts add claude")
	}
	if claudeFresh(c) {
		return c.OAuth.AccessToken, nil
	}
	if c.OAuth.RefreshToken == "" {
		return "", errors.New("Claude Code OAuth token expired; run claude auth login")
	}
	fresh, err := claudeRefresh(ctx, c)
	if err != nil {
		return "", err
	}
	out, err := fresh.marshal()
	if err != nil {
		return "", err
	}
	if err := writeSecretFile(path, append(out, '\n')); err != nil {
		return "", err
	}
	return fresh.OAuth.AccessToken, nil
}

// claudeHeldSubscriptionUsage is claudeSubscriptionUsage for an account magpie
// keeps at path.
func claudeHeldSubscriptionUsage(ctx context.Context, path, plan string) SubscriptionQuota {
	q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Windows: []QuotaWindow{}}
	token, err := claudeHeldToken(ctx, path)
	if err != nil {
		q.Error = err.Error()
		return q
	}
	return claudeUsageWith(ctx, q, token, plan)
}

// usageSource is one remembered account and how to ask for its usage.
type usageSource struct {
	user  string
	fetch func(ctx context.Context) SubscriptionQuota
}

// usageSources lists the remembered accounts whose usage can be asked for now.
func usageSources(agent string) []usageSource {
	idx := loadAccountIndex()
	liveUser, _, live := liveIdentity(agent)
	home, _ := os.UserHomeDir()
	var out []usageSource
	for _, r := range idx.Accounts {
		if r.Agent != agent {
			continue
		}
		user, plan := r.User, r.Plan
		path := secretPath(agent, user)
		var fetch func(ctx context.Context) SubscriptionQuota
		switch {
		case live && strings.EqualFold(user, liveUser):
			if agent == "claude" {
				fetch = claudeSubscriptionUsage
			} else {
				auth := codexAuthPath(home)
				fetch = func(ctx context.Context) SubscriptionQuota { return codexSubscriptionUsage(ctx, auth) }
			}
		case fileExists(path):
			if agent == "claude" {
				fetch = func(ctx context.Context) SubscriptionQuota { return claudeHeldSubscriptionUsage(ctx, path, plan) }
			} else {
				fetch = func(ctx context.Context) SubscriptionQuota { return codexSubscriptionUsage(ctx, path) }
			}
		default:
			continue
		}
		out = append(out, usageSource{user: user, fetch: fetch})
	}
	return out
}

// loginUsageEntry is the last LoginUsage answer for one agent.
type loginUsageEntry struct {
	at   time.Time
	data map[string]SubscriptionQuota
}

var (
	loginUsageMu    sync.Mutex
	loginUsageCache = map[string]loginUsageEntry{}
)

// LoginUsage reports each remembered account's own 5-hour and weekly usage,
// keyed by email. An answer is reused for a minute. An account whose query fails
// keeps what was last known about it. An agent without per-account usage gets
// an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	canon, ok := AccountAgent(agent)
	if !ok {
		return map[string]SubscriptionQuota{}
	}
	loginUsageMu.Lock()
	defer loginUsageMu.Unlock()
	prev := loginUsageCache[canon]
	if prev.data != nil && time.Since(prev.at) < time.Minute {
		return copyQuotas(prev.data)
	}
	out := map[string]SubscriptionQuota{}
	for _, src := range usageSources(canon) {
		qctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
		q := src.fetch(qctx)
		cancel()
		if old, known := prev.data[src.user]; known && q.Error != "" {
			out[src.user] = old
			continue
		}
		out[src.user] = q
	}
	loginUsageCache[canon] = loginUsageEntry{at: time.Now(), data: out}
	return copyQuotas(out)
}

func copyQuotas(in map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	out := make(map[string]SubscriptionQuota, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
