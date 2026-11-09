package provider

// Remembered sign-ins: the subscription accounts magpie has seen an agent
// signed in to. The user can switch between them without signing in again,
// and any number of them can serve requests at the same time.
//
// The file is accounts.json beside providers.json, mode 0600. Each account's
// credentials have exactly one holder. While the agent is signed in as an
// account, the agent's own file (or keychain item) holds them and the entry
// keeps only metadata. Every other remembered account keeps its credentials
// here, so a token refresh rotates them in one place.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// savedAccount is one remembered sign-in.
type savedAccount struct {
	Agent   string          `json:"agent"`
	User    string          `json:"user"`
	Plan    string          `json:"plan,omitempty"`
	On      bool            `json:"on,omitempty"`      // serves requests alongside the agent's current login
	Profile json.RawMessage `json:"profile,omitempty"` // claude: the oauthAccount block Claude Code keeps
	Cred    json.RawMessage `json:"cred,omitempty"`    // the agent's credential file; empty while the agent is signed in as this account
}

type savedFile struct {
	Accounts []savedAccount `json:"accounts"`
}

// accountsMu serializes read-modify-write of accounts.json.
var accountsMu sync.Mutex

// agentLogin runs the agent's own sign-in, which opens the vendor's page; a
// var so tests can stand in for the browser.
var agentLogin = runAgentLogin

// accountsPath is accounts.json, beside providers.json.
func accountsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

func loadAccounts() savedFile {
	var f savedFile
	if b, err := os.ReadFile(accountsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func storeAccounts(f savedFile) error {
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

// storedCred returns the credential file magpie keeps for a remembered account.
func storedCred(agent, user string) ([]byte, bool) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := indexOf(f.Accounts, agent, user)
	if i < 0 || len(f.Accounts[i].Cred) == 0 {
		return nil, false
	}
	return append([]byte(nil), f.Accounts[i].Cred...), true
}

// saveStoredCred replaces the credential file magpie keeps for a remembered account.
func saveStoredCred(agent, user string, b []byte) error {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := indexOf(f.Accounts, agent, user)
	if i < 0 {
		return fmt.Errorf("%s account %s is no longer remembered", agent, user)
	}
	f.Accounts[i].Cred = append([]byte(nil), b...)
	return storeAccounts(f)
}

// agentID maps an agent name the user typed to the agent's id, or says which names exist.
func agentID(name string) (string, error) {
	if id, ok := AccountAgent(name); ok {
		return id, nil
	}
	return "", fmt.Errorf("unknown agent %q: use claude (cc) or codex", name)
}

// AccountAgent maps the agent names the CLI accepts to the agent's id.
func AccountAgent(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

// LoginAgents lists the agents whose sign-ins magpie remembers, in display order.
var LoginAgents = []string{"claude", "codex"}

func sameUser(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func indexOf(list []savedAccount, agent, user string) int {
	for i, e := range list {
		if e.Agent == agent && sameUser(e.User, user) {
			return i
		}
	}
	return -1
}

// compactJSON makes JSON comparable regardless of indentation.
func compactJSON(b []byte) []byte {
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return b
	}
	return buf.Bytes()
}

// rememberLogins records the agents' current sign-ins. Their credentials stay
// where the agent keeps them, so any copy magpie had is dropped.
func rememberLogins(logins []Provider) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	changed := false
	for _, p := range logins {
		if p.Account == nil || !isLoginAgent(p.Account.Agent) {
			continue
		}
		a := p.Account
		var profile json.RawMessage
		if a.Agent == "claude" {
			profile, _ = claudeProfile()
		}
		i := indexOf(f.Accounts, a.Agent, a.User)
		if i < 0 {
			f.Accounts = append(f.Accounts, savedAccount{Agent: a.Agent, User: a.User, Plan: a.Plan, Profile: profile})
			changed = true
			continue
		}
		e := &f.Accounts[i]
		if e.Plan != a.Plan || (a.Agent == "claude" && !bytes.Equal(compactJSON(e.Profile), compactJSON(profile))) {
			e.Plan, e.Profile = a.Plan, profile
			changed = true
		}
		if len(e.Cred) > 0 {
			e.Cred = nil
			changed = true
		}
	}
	if changed {
		storeAccounts(f)
	}
}

func isLoginAgent(agent string) bool {
	return agent == "claude" || agent == "codex"
}

// currentSignIn is the agent's own current login, if it has one.
func currentSignIn(agent string) (Provider, bool) {
	for _, p := range Accounts() {
		if p.Account != nil && p.Account.Agent == agent {
			return p, true
		}
	}
	return Provider{}, false
}

// isCurrent reports whether a remembered account is the agent's current login.
func isCurrent(cur []Provider, e savedAccount) bool {
	for _, p := range cur {
		if p.Account != nil && p.Account.Agent == e.Agent && sameUser(p.Account.User, e.User) {
			return true
		}
	}
	return false
}

// savedProvider turns a remembered account that holds credentials into a
// provider with its own id, signing with its own tokens.
func savedProvider(e savedAccount) (Provider, bool) {
	slug := Slug(e.User)
	if slug == "" || len(e.Cred) == 0 {
		return Provider{}, false
	}
	id := e.Agent + "-" + slug
	switch e.Agent {
	case "claude":
		return claudeProvider(id, "Claude Code ("+e.User+")", e.User, e.Plan, storedClaudeSource(e.User)), true
	case "codex":
		return codexProvider(id, "Codex ("+e.User+")", e.User, e.Plan, storedCodexSource(e.User)), true
	}
	return Provider{}, false
}

// accountProviders lists the signed-in accounts as providers, in gateway
// order: the agents' current logins first, then the remembered accounts that
// are on, in the order they were saved. With all set, every remembered
// account that holds credentials is listed, on or not.
func accountProviders(all bool) []Provider {
	cur := Accounts()
	out := append([]Provider(nil), cur...)
	for _, e := range loadAccounts().Accounts {
		if !isLoginAgent(e.Agent) || (!all && !e.On) || isCurrent(cur, e) {
			continue
		}
		if p, ok := savedProvider(e); ok {
			out = append(out, p)
		}
	}
	return out
}

// AccountPeers lists the other accounts that serve requests alongside p, in
// the order the gateway tries them: the agent's current login first, then the
// rest in the order they were saved.
func AccountPeers(p Provider) []Provider {
	if p.Account == nil {
		return nil
	}
	var out []Provider
	for _, q := range accountProviders(false) {
		if q.Account != nil && q.Account.Agent == p.Account.Agent && q.ID != p.ID {
			out = append(out, q)
		}
	}
	return out
}

// SignIn is one account an agent has been signed in to, as `magpie accounts` shows it.
type SignIn struct {
	Agent   string
	User    string
	Plan    string
	Current bool // the agent is signed in as this account
	On      bool // serves requests through the gateway; the current login always does
}

// SignIns lists the accounts remembered for an agent: the current login first,
// then the others in the order they were saved.
func SignIns(agent string) []SignIn {
	cur := Accounts()
	var out []SignIn
	for _, p := range cur {
		if p.Account != nil && p.Account.Agent == agent {
			out = append(out, SignIn{Agent: agent, User: p.Account.User, Plan: p.Account.Plan, Current: true, On: true})
		}
	}
	for _, e := range loadAccounts().Accounts {
		if e.Agent == agent && !isCurrent(cur, e) {
			out = append(out, SignIn{Agent: agent, User: e.User, Plan: e.Plan, On: e.On})
		}
	}
	return out
}

// SetLoginOn makes a remembered account serve requests alongside the others,
// or stops it. The agent's current login always serves, so it cannot be stopped.
func SetLoginOn(agent, user string, on bool) error {
	agent, err := agentID(agent)
	if err != nil {
		return err
	}
	cur, hasCur := currentSignIn(agent)
	if hasCur && sameUser(cur.Account.User, user) {
		if !on {
			return fmt.Errorf("%s is the account %s is signed in as, and it always serves requests; sign in to another account to stop it", user, agent)
		}
		return nil
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := indexOf(f.Accounts, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agent, user, agent)
	}
	if on && len(f.Accounts[i].Cred) == 0 {
		return fmt.Errorf("%s has no sign-in left; sign in to it again with magpie accounts add %s", user, agent)
	}
	f.Accounts[i].On = on
	return storeAccounts(f)
}

// ForgetLogin drops a remembered account. The agent's current login cannot be forgotten.
func ForgetLogin(agent, user string) error {
	agent, err := agentID(agent)
	if err != nil {
		return err
	}
	if cur, ok := currentSignIn(agent); ok && sameUser(cur.Account.User, user) {
		return fmt.Errorf("%s is the account %s is signed in as; switch to another account before forgetting it", user, agent)
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := indexOf(f.Accounts, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agent, user, agent)
	}
	f.Accounts = append(f.Accounts[:i], f.Accounts[i+1:]...)
	return storeAccounts(f)
}

// SwitchLogin signs the agent in to a remembered account. The account the agent
// was signed in to is remembered with its latest credentials, and takes the
// on-state of the account it gives way to. The agent's other files keep their
// content.
func SwitchLogin(agent, user string) error {
	agent, err := agentID(agent)
	if err != nil {
		return err
	}
	cur, hasCur := currentSignIn(agent)
	if hasCur && sameUser(cur.Account.User, user) {
		return nil
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	ti := indexOf(f.Accounts, agent, user)
	if ti < 0 {
		return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agent, user, agent)
	}
	target := f.Accounts[ti]
	if len(target.Cred) == 0 {
		return fmt.Errorf("%s has no sign-in left; sign in to it again with magpie accounts add %s", user, agent)
	}
	var outCred, outProfile []byte
	if hasCur {
		var err error
		if outCred, outProfile, err = readLogin(agent); err != nil {
			return err
		}
	}
	if err := writeLogin(agent, target); err != nil {
		return err
	}
	if hasCur {
		j := indexOf(f.Accounts, agent, cur.Account.User)
		if j < 0 {
			f.Accounts = append(f.Accounts, savedAccount{Agent: agent, User: cur.Account.User})
			j = len(f.Accounts) - 1
		}
		e := &f.Accounts[j]
		e.Cred, e.Plan, e.On = outCred, cur.Account.Plan, target.On
		if agent == "claude" {
			e.Profile = outProfile
		}
		ti = indexOf(f.Accounts, agent, user)
	}
	f.Accounts[ti].Cred = nil
	forgetClaudeCredential()
	forgetClaudeStatus()
	return storeAccounts(f)
}

// readLogin returns the agent's current credentials as magpie keeps them, and
// for Claude Code the account block from its config file.
func readLogin(agent string) (cred, profile []byte, err error) {
	if agent == "codex" {
		b, err := os.ReadFile(codexAuthPath())
		if err != nil {
			return nil, nil, fmt.Errorf("read Codex sign-in: %w", err)
		}
		return b, nil, nil
	}
	c, _, ok := readClaudeCredential()
	if !ok {
		return nil, nil, errors.New("Claude Code is not signed in; nothing to keep for the account it leaves")
	}
	b, err := c.marshal()
	if err != nil {
		return nil, nil, err
	}
	profile, _ = claudeProfile()
	return b, profile, nil
}

// writeLogin signs the agent in as a remembered account. Only the sign-in
// fields change; the rest of the agent's files stay as they are.
func writeLogin(agent string, e savedAccount) error {
	if agent == "codex" {
		return writeCodexLogin(e)
	}
	return writeClaudeLogin(e)
}

func writeClaudeLogin(e savedAccount) error {
	tc, ok := parseClaudeCredentials(e.Cred)
	if !ok {
		return fmt.Errorf("the saved sign-in for %s is unreadable", e.User)
	}
	c, loc, ok := readClaudeCredential()
	if !ok {
		c = claudeCredentials{raw: map[string]any{}}
		loc = claudeDefaultLocation()
	}
	if c.raw == nil {
		c.raw = map[string]any{}
	}
	if block, ok := tc.raw["claudeAiOauth"]; ok {
		c.raw["claudeAiOauth"] = block
	}
	c.OAuth = tc.OAuth
	if err := saveClaudeCredential(loc, c); err != nil {
		return err
	}
	return writeClaudeProfile(e.Profile)
}

// writeClaudeProfile sets the oauthAccount block in Claude Code's config file.
func writeClaudeProfile(profile json.RawMessage) error {
	if len(profile) == 0 {
		return nil
	}
	var block any
	if err := json.Unmarshal(profile, &block); err != nil {
		return err
	}
	return updateJSONFile(claudeJSONPath(), func(raw map[string]any) {
		raw["oauthAccount"] = block
	})
}

func writeCodexLogin(e savedAccount) error {
	tgt, err := decodeRaw(e.Cred)
	if err != nil {
		return fmt.Errorf("the saved sign-in for %s is unreadable", e.User)
	}
	return updateJSONFile(codexAuthPath(), func(raw map[string]any) {
		for _, k := range []string{"auth_mode", "tokens", "last_refresh"} {
			if v, ok := tgt[k]; ok {
				raw[k] = v
			} else {
				delete(raw, k)
			}
		}
	})
}

// updateJSONFile edits the top-level object of a JSON file in place, keeping
// every field the edit does not touch. A file that cannot be read is left alone.
func updateJSONFile(path string, edit func(map[string]any)) error {
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if raw, err = decodeRaw(b); err != nil {
			return fmt.Errorf("%s is not valid JSON; not touching it: %w", path, err)
		}
	}
	edit(raw)
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// decodeRaw parses a JSON object without losing number precision.
func decodeRaw(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not a JSON object")
	}
	return m, nil
}

// fileState is a file as it was before a sign-in changed it.
type fileState struct {
	data    []byte
	existed bool
}

// AddAccount opens the agent's own sign-in and remembers the account it signs
// in to. The account the agent was signed in to stays signed in, and the new
// one serves requests alongside it. It returns the new account's name.
func AddAccount(agent string) (string, error) {
	agent, err := agentID(agent)
	if err != nil {
		return "", err
	}
	prev, hadPrev := currentSignIn(agent)
	paths := []string{codexAuthPath()}
	var kc *claudeCredentials
	var kcLoc claudeCredentialLocation
	if agent == "claude" {
		paths = []string{claudeCredPath(), claudeJSONPath()}
		if c, loc, ok := readClaudeCredential(); ok && loc.keychain {
			kc, kcLoc = &c, loc
		}
	}
	saved := map[string]fileState{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		saved[p] = fileState{data: b, existed: err == nil}
	}
	restore := func() {
		for p, s := range saved {
			if s.existed {
				os.WriteFile(p, s.data, 0o600)
			} else {
				os.Remove(p)
			}
		}
		if kc != nil {
			saveClaudeCredential(kcLoc, *kc)
		}
		forgetClaudeCredential()
		forgetClaudeStatus()
	}

	forgetClaudeCredential()
	forgetClaudeStatus()
	loginErr := agentLogin(agent)
	forgetClaudeCredential()
	forgetClaudeStatus()
	var fresh Provider
	var found bool
	if loginErr == nil {
		fresh, found = freshSignIn(agent)
	}
	if !found {
		if hadPrev {
			restore()
		}
		if loginErr != nil {
			return "", fmt.Errorf("%s sign-in did not finish: %w", agent, loginErr)
		}
		return "", fmt.Errorf("%s did not sign in to an account", agent)
	}
	cred, profile, err := readLogin(agent)
	if err != nil {
		if hadPrev {
			restore()
		}
		return "", err
	}
	user := fresh.Account.User
	if hadPrev && !sameUser(prev.Account.User, user) {
		// the new sign-in leaves the agent; the previous one goes back
		restore()
		accountsMu.Lock()
		f := loadAccounts()
		i := indexOf(f.Accounts, agent, user)
		if i < 0 {
			f.Accounts = append(f.Accounts, savedAccount{Agent: agent})
			i = len(f.Accounts) - 1
		}
		e := &f.Accounts[i]
		e.User, e.Plan, e.On, e.Cred, e.Profile = user, fresh.Account.Plan, true, cred, profile
		err = storeAccounts(f)
		accountsMu.Unlock()
		if err != nil {
			return "", err
		}
		return user, nil
	}
	return user, nil
}

// freshSignIn reads the agent's login without the caches, and without
// remembering it.
func freshSignIn(agent string) (Provider, bool) {
	if agent == "codex" {
		home, _ := os.UserHomeDir()
		return codexAccount(home)
	}
	return claudeAccount()
}

// runAgentLogin runs the agent's own sign-in, which opens the vendor's page.
func runAgentLogin(agent string) error {
	var name string
	var args []string
	switch agent {
	case "claude":
		name = claudeExecutable()
		args = []string{"auth", "login"}
		if name == "" {
			return errors.New("Claude Code is not installed")
		}
	case "codex":
		p, err := exec.LookPath("codex")
		if err != nil {
			return errors.New("Codex is not installed")
		}
		name, args = p, []string{"login"}
	default:
		return fmt.Errorf("unknown agent %q", agent)
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// LoginPage is the vendor page an agent's sign-in opens, for the user to see.
func LoginPage(agent string) string {
	if agent == "codex" {
		return "https://chatgpt.com"
	}
	return "https://claude.ai"
}
