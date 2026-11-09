package provider

// Remembered sign-ins: the Claude Code and Codex accounts the user has signed
// in to, so they can be switched between and, when switched on, used side by
// side.
//
// The agent keeps its current sign-in where it always did (Codex's auth.json,
// Claude Code's .credentials.json). Every other remembered account keeps its
// credentials in a file of magpie's own, mode 0600, under <config>/accounts.
// A refresh token therefore has exactly one holder: the agent for the current
// account, magpie's file for the rest. Switching moves the tokens between the
// two; nothing is ever written into the agent's store for a switched-on
// account.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

// Login is one account of a subscription agent, as magpie remembers it.
type Login struct {
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Plan    string `json:"plan,omitempty"`
	Current bool   `json:"current"` // the agent is signed in as this account
	On      bool   `json:"on"`      // serves gateway requests; the current one always does
}

// loginRecord is what logins.json keeps per account. Whether magpie holds
// its credentials is told by the credentials file, not by the record.
type loginRecord struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	On    bool   `json:"on,omitempty"`
}

type loginFile struct {
	Logins []loginRecord `json:"logins"`
}

// loginAgents are the agents whose sign-ins magpie can remember and switch.
var loginAgents = []string{"claude", "codex"}

// loginMu guards logins.json and the credentials files of remembered accounts.
// It is taken before an agent's own lock (claudeMu, codexMu), never after.
var loginMu sync.Mutex

// signInMarker is left in the accounts directory while `accounts add` has the
// agent's sign-in page open. Until it goes, the agent's current sign-in is not
// refreshed and magpie's copy of it is not dropped, so the copy stays valid: a
// refresh would rotate the token the copy holds. The marker is only honoured
// for a quarter of an hour, in case the command was killed. It is a file, not
// a flag, because the gateway may run in another process.
func signInMarker(agent string) string { return filepath.Join(loginDir(), "signing-in-"+agent) }

func signingIn(agent string) bool {
	st, err := os.Stat(signInMarker(agent))
	return err == nil && time.Since(st.ModTime()) < 15*time.Minute
}

// CanonicalAgent maps an agent name the user typed to the id magpie keeps:
// claude (and its alias cc), or codex.
func CanonicalAgent(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func unknownAgent(name string) error {
	return fmt.Errorf("%q is not an agent with remembered accounts; use claude (cc) or codex", name)
}

func loginDir() string   { return filepath.Join(filepath.Dir(Path()), "accounts") }
func loginIndex() string { return filepath.Join(loginDir(), "logins.json") }

// loginCreds is where magpie keeps a remembered account's credentials. The
// name is a fingerprint of the account, so no address appears in a file name.
func loginCreds(agent, user string) string {
	return filepath.Join(loginDir(), agent+"-"+loginRef(agent, user)+".json")
}

// loginRef identifies an account to the gateway's cooldowns and call logs.
func loginRef(agent, user string) string {
	return KeyRef(agent + "\x00" + strings.ToLower(user))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readLogins() []loginRecord {
	var f loginFile
	if b, err := os.ReadFile(loginIndex()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f.Logins
}

func writeLogins(rs []loginRecord) error {
	if err := os.MkdirAll(loginDir(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(loginFile{Logins: rs}, "", "  ")
	if err != nil {
		return err
	}
	p := loginIndex()
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// writeCreds stores credentials readable by the user only.
func writeCreds(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// findLogin matches an account by its address, ignoring case.
func findLogin(rs []loginRecord, agent, user string) (int, bool) {
	for i, r := range rs {
		if r.Agent == agent && strings.EqualFold(r.User, user) {
			return i, true
		}
	}
	return -1, false
}

// upsertLogin replaces the record for the same account, or appends it.
func upsertLogin(rs []loginRecord, r loginRecord) []loginRecord {
	if i, ok := findLogin(rs, r.Agent, r.User); ok {
		rs[i] = r
		return rs
	}
	return append(rs, r)
}

// currentIn is the account the agent is signed in as, if any.
func currentIn(accts []Provider, agent string) (Provider, bool) {
	for _, a := range accts {
		if a.Account != nil && a.Account.Agent == agent {
			return a, true
		}
	}
	return Provider{}, false
}

// agentMutex is the lock that serializes the agent's own credential store.
func agentMutex(agent string) *sync.Mutex {
	if agent == "claude" {
		return &claudeMu
	}
	return &codexMu
}

// agentCreds reads the credentials the agent keeps for its current sign-in,
// in the form loginCreds stores them. The caller holds the agent's lock.
func agentCreds(agent string) ([]byte, error) {
	switch agent {
	case "claude":
		c, _, ok := readClaudeCredential()
		if !ok {
			return nil, errors.New("Claude Code is signed out")
		}
		return c.marshal()
	case "codex":
		return os.ReadFile(codexAuthFile())
	}
	return nil, unknownAgent(agent)
}

// restoreAgentCreds makes a remembered account the agent's current sign-in.
// Only the sign-in itself is replaced; every other field the agent keeps
// stays as it is. The caller holds the agent's lock.
func restoreAgentCreds(agent string, blob []byte) error {
	var saved map[string]any
	if json.Unmarshal(blob, &saved) != nil {
		return errors.New("the saved sign-in is unreadable")
	}
	switch agent {
	case "claude":
		parsed, ok := parseClaudeCredentials(blob)
		if !ok {
			return errors.New("the saved Claude Code sign-in has no access token")
		}
		cur, loc, ok := readClaudeCredential()
		if !ok {
			cur = claudeCredentials{}
			loc = claudeCredentialLocation{path: filepath.Join(claudeConfigDir(), ".credentials.json")}
		}
		if cur.raw == nil {
			cur.raw = map[string]any{}
		}
		cur.raw["claudeAiOauth"] = saved["claudeAiOauth"]
		cur.OAuth = parsed.OAuth
		return saveClaudeCredential(loc, cur)
	case "codex":
		var cur map[string]any
		if !readJSON(codexAuthFile(), &cur) || cur == nil {
			cur = map[string]any{}
		}
		for _, k := range []string{"tokens", "auth_mode", "last_refresh"} {
			if v, ok := saved[k]; ok {
				cur[k] = v
			} else {
				delete(cur, k)
			}
		}
		out, err := json.MarshalIndent(cur, "", "  ")
		if err != nil {
			return err
		}
		return writeCreds(codexAuthFile(), out)
	}
	return unknownAgent(agent)
}

// keepCurrentLocked copies the agent's current sign-in into magpie's file for
// that account, before something replaces it. The caller holds loginMu.
func keepCurrentLocked(agent, user string) error {
	mu := agentMutex(agent)
	mu.Lock()
	defer mu.Unlock()
	b, err := agentCreds(agent)
	if err != nil {
		return err
	}
	return writeCreds(loginCreds(agent, user), b)
}

// rememberLocked brings logins.json in line with the agents: the current
// sign-in is remembered, its credentials file is dropped (the agent holds it
// now), and remembered accounts whose credentials are gone are forgotten.
// The caller holds loginMu.
func rememberLocked(accts []Provider) {
	rs := readLogins()
	before := append([]loginRecord(nil), rs...)
	for _, agent := range loginAgents {
		cur, ok := currentIn(accts, agent)
		if !ok {
			continue
		}
		i, known := findLogin(rs, agent, cur.Account.User)
		if !known {
			rs = append(rs, loginRecord{Agent: agent, User: cur.Account.User})
			i = len(rs) - 1
		}
		rs[i].Plan = cur.Account.Plan
		if !signingIn(agent) {
			os.Remove(loginCreds(agent, cur.Account.User))
		}
	}
	var keep []loginRecord
	for _, r := range rs {
		cur, hasCur := currentIn(accts, r.Agent)
		isCurrent := hasCur && strings.EqualFold(cur.Account.User, r.User)
		if isCurrent || fileExists(loginCreds(r.Agent, r.User)) {
			keep = append(keep, r)
		}
	}
	if !reflect.DeepEqual(before, keep) {
		writeLogins(keep)
	}
}

// loginsOf lists an agent's remembered accounts in the order they were saved.
func loginsOf(agent string, accts []Provider, rs []loginRecord) []Login {
	cur, hasCur := currentIn(accts, agent)
	var out []Login
	for _, r := range rs {
		if r.Agent != agent {
			continue
		}
		l := Login{Agent: agent, User: r.User, Plan: r.Plan, On: r.On}
		if hasCur && strings.EqualFold(cur.Account.User, r.User) {
			l.Current, l.On = true, true
			if cur.Account.Plan != "" {
				l.Plan = cur.Account.Plan
			}
		}
		out = append(out, l)
	}
	return out
}

// Logins lists the remembered accounts of an agent, marking the one the agent
// is signed in as.
func Logins(agent string) []Login {
	name, ok := CanonicalAgent(agent)
	if !ok {
		return nil
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	accts := Accounts()
	rememberLocked(accts)
	return loginsOf(name, accts, readLogins())
}

// SwitchLogin signs the agent in as a remembered account. The account the
// agent was signed in as is kept, with its latest credentials, and takes over
// the switched-on state of the account it replaces. Sessions already running
// keep the account they started with until they restart.
func SwitchLogin(agent, user string) error {
	name, ok := CanonicalAgent(agent)
	if !ok {
		return unknownAgent(agent)
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	mu := agentMutex(name)
	mu.Lock()
	defer mu.Unlock()

	accts := Accounts()
	rememberLocked(accts)
	rs := readLogins()
	i, ok := findLogin(rs, name, user)
	if !ok {
		return fmt.Errorf("no remembered %s account %s; magpie accounts lists them", name, user)
	}
	target := rs[i]
	cur, hasCur := currentIn(accts, name)
	if hasCur && strings.EqualFold(cur.Account.User, target.User) {
		return nil
	}
	blob, err := os.ReadFile(loginCreds(name, target.User))
	if err != nil {
		return fmt.Errorf("no saved sign-in for %s; run magpie accounts add %s to sign in again", target.User, name)
	}
	if hasCur {
		b, err := agentCreds(name)
		if err != nil {
			return err
		}
		if err := writeCreds(loginCreds(name, cur.Account.User), b); err != nil {
			return err
		}
		rs = upsertLogin(rs, loginRecord{Agent: name, User: cur.Account.User, Plan: cur.Account.Plan, On: target.On})
	}
	if err := restoreAgentCreds(name, blob); err != nil {
		return err
	}
	os.Remove(loginCreds(name, target.User))
	forgetClaudeCredential()
	forgetClaudeStatus()
	return writeLogins(rs)
}

// ForgetLogin drops a remembered account. The account the agent is signed in
// as cannot be forgotten; sign out in the agent for that.
func ForgetLogin(agent, user string) error {
	name, ok := CanonicalAgent(agent)
	if !ok {
		return unknownAgent(agent)
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	accts := Accounts()
	rememberLocked(accts)
	rs := readLogins()
	i, ok := findLogin(rs, name, user)
	if !ok {
		return fmt.Errorf("no remembered %s account %s; magpie accounts lists them", name, user)
	}
	if cur, ok := currentIn(accts, name); ok && strings.EqualFold(cur.Account.User, rs[i].User) {
		return fmt.Errorf("%s is the %s account the agent is signed in as; sign out in the agent first", user, name)
	}
	os.Remove(loginCreds(name, rs[i].User))
	var keep []loginRecord
	for j, r := range rs {
		if j != i {
			keep = append(keep, r)
		}
	}
	return writeLogins(keep)
}

// SetLoginOn lets a remembered account serve gateway requests alongside the
// current one, or stops it. The current account always serves.
func SetLoginOn(agent, user string, on bool) error {
	name, ok := CanonicalAgent(agent)
	if !ok {
		return unknownAgent(agent)
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	accts := Accounts()
	rememberLocked(accts)
	rs := readLogins()
	i, ok := findLogin(rs, name, user)
	if !ok {
		return fmt.Errorf("no remembered %s account %s; magpie accounts lists them", name, user)
	}
	if cur, ok := currentIn(accts, name); ok && strings.EqualFold(cur.Account.User, rs[i].User) {
		if !on {
			return fmt.Errorf("%s is the account the agent is signed in as, and always serves; switch to another account first", user)
		}
		return nil
	}
	rs[i].On = on
	return writeLogins(rs)
}

// AddLogin opens the agent's sign-in page through login, which must run it,
// and remembers the account it leaves signed in. The account being replaced
// is kept first. An account added beside an existing one is switched on.
func AddLogin(agent string, login func() error) ([]Login, error) {
	name, ok := CanonicalAgent(agent)
	if !ok {
		return nil, unknownAgent(agent)
	}
	loginMu.Lock()
	accts := Accounts()
	rememberLocked(accts)
	known := map[string]bool{}
	for _, r := range readLogins() {
		if r.Agent == name {
			known[strings.ToLower(r.User)] = true
		}
	}
	if cur, ok := currentIn(accts, name); ok {
		if err := keepCurrentLocked(name, cur.Account.User); err != nil {
			loginMu.Unlock()
			return nil, err
		}
	}
	if err := os.MkdirAll(loginDir(), 0o700); err != nil {
		loginMu.Unlock()
		return nil, err
	}
	if err := os.WriteFile(signInMarker(name), nil, 0o600); err != nil {
		loginMu.Unlock()
		return nil, err
	}
	loginMu.Unlock()

	err := login()

	loginMu.Lock()
	os.Remove(signInMarker(name))
	accts = Accounts()
	rememberLocked(accts)
	rs := readLogins()
	if cur, ok := currentIn(accts, name); ok && !known[strings.ToLower(cur.Account.User)] {
		others := 0
		for _, r := range rs {
			if r.Agent == name && !strings.EqualFold(r.User, cur.Account.User) {
				others++
			}
		}
		if i, found := findLogin(rs, name, cur.Account.User); found && others > 0 {
			rs[i].On = true
			writeLogins(rs)
		}
	}
	loginMu.Unlock()
	return Logins(name), err
}

// savedLogins lists the remembered accounts of an agent that magpie holds
// credentials for, other than the current one, in the order they were saved.
// With onlyOn set, only the switched-on ones.
func savedLogins(agent, current string, onlyOn bool) []loginRecord {
	loginMu.Lock()
	defer loginMu.Unlock()
	var out []loginRecord
	for _, r := range readLogins() {
		if r.Agent != agent || strings.EqualFold(r.User, current) || (onlyOn && !r.On) {
			continue
		}
		if fileExists(loginCreds(agent, r.User)) {
			out = append(out, r)
		}
	}
	return out
}

// accountCredentials lists the ways a subscription account can be called: the
// current sign-in first, then the other switched-on sign-ins in the order they
// were saved. Only Codex has more than one: a Claude Code subscription runs
// through the Claude Code binary, which reads the current sign-in alone.
func accountCredentials(p Provider) []Credential {
	a := p.Account
	out := []Credential{{Ref: loginRef(a.Agent, a.User), Name: a.User, Account: a}}
	if a.Agent != "codex" {
		return out
	}
	for _, r := range savedLogins(a.Agent, a.User, true) {
		out = append(out, Credential{Ref: loginRef(r.Agent, r.User), Name: r.User,
			Account: codexSigner(loginCreds(r.Agent, r.User), r.User, r.Plan)})
	}
	return out
}

// loginUsageCache keeps each agent's per-account usage for a minute.
var loginUsageCache struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota
}

// LoginUsage reports the rolling allowance use of every remembered account of
// an agent, keyed by address. A result is reused for a minute. When an account
// cannot be read now, its earlier result is kept. Agents without per-account
// usage give an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	name, ok := CanonicalAgent(agent)
	if !ok {
		return out
	}
	c := &loginUsageCache
	c.Lock()
	fresh := time.Since(c.at[name]) < time.Minute
	if fresh {
		for k, v := range c.data[name] {
			out[k] = v
		}
	}
	c.Unlock()
	if fresh {
		return out
	}

	got := fetchLoginUsage(ctx, name)

	c.Lock()
	defer c.Unlock()
	prev := c.data[name]
	merged := map[string]SubscriptionQuota{}
	for user, q := range got {
		if old, ok := prev[user]; ok && q.Error != "" {
			merged[user] = old
			continue
		}
		merged[user] = q
	}
	if c.at == nil {
		c.at = map[string]time.Time{}
	}
	if c.data == nil {
		c.data = map[string]map[string]SubscriptionQuota{}
	}
	c.at[name], c.data[name] = time.Now(), merged
	for k, v := range merged {
		out[k] = v
	}
	return out
}

// fetchLoginUsage asks the vendor about every remembered account of an agent.
// Claude Code's usage endpoint answers for the current sign-in only.
func fetchLoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	accts := Accounts()
	cur, hasCur := currentIn(accts, agent)
	current := ""
	if hasCur {
		current = cur.Account.User
		if agent == "codex" {
			out[current] = codexSubscriptionUsage(ctx, codexAuthFile())
		} else {
			out[current] = claudeSubscriptionUsage(ctx)
		}
	}
	if agent == "codex" {
		for _, r := range savedLogins(agent, current, false) {
			out[r.User] = codexSubscriptionUsage(ctx, loginCreds(agent, r.User))
		}
	}
	return out
}
