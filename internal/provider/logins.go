package provider

// Remembered logins: the Claude Code and Codex (ChatGPT) accounts the user has
// signed in to, so magpie can switch between them and keep several in use.
//
// The agent holds the tokens of the account it is signed in as, and only that
// account's tokens live in the agent's own store. Every other remembered
// account keeps its tokens in accounts/accounts.json (directory 0700, file
// 0600). A refresh token thus has one holder at a time: switching moves
// tokens out of the agent into the file, and the target's tokens from the file
// into the agent. Only the login fields are replaced in the agent's files; the
// rest of them is left as it was.

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

// Login is a remembered account, as listed.
type Login struct {
	Agent   string // "claude" or "codex"
	User    string // the account's email (or the label magpie gave it)
	Plan    string // the subscription, when known
	Current bool   // the agent is signed in as this account
	On      bool   // used alongside the signed-in account; always true for Current
}

// loginRecord is one remembered account as stored.
type loginRecord struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	On    bool   `json:"on,omitempty"`
	// Creds are the agent's login fields while the agent is signed out of
	// this account. Absent while the agent holds the account's tokens.
	Creds json.RawMessage `json:"creds,omitempty"`
}

type loginFile struct {
	Accounts []loginRecord `json:"accounts"`
}

// loginAgent is an agent whose sign-ins magpie can remember.
type loginAgent struct {
	id   string
	name string
	// signedIn says who the agent is signed in as and returns the login
	// fields that identify that sign-in; ok is false when it is signed out.
	signedIn func() (user, plan string, creds json.RawMessage, ok bool)
	// sign replaces the agent's login fields with creds, keeping everything else.
	sign func(creds json.RawMessage) error
}

var loginMu sync.Mutex

// claudeWho says whom Claude Code is signed in as; a var so tests can answer.
var claudeWho = claudeIdentity

func loginAgents() []loginAgent {
	return []loginAgent{
		{id: "claude", name: "Claude Code", signedIn: claudeSignedIn, sign: claudeSign},
		{id: "codex", name: "Codex", signedIn: codexSignedIn, sign: codexSign},
	}
}

// CanonicalLoginAgent maps an agent name (or alias) to the id magpie remembers
// logins under. Only claude (cc) and codex have remembered logins.
func CanonicalLoginAgent(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc", "claude-code":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("unknown agent %q", name)
}

func loginAgentFor(name string) (loginAgent, error) {
	id, err := CanonicalLoginAgent(name)
	if err != nil {
		return loginAgent{}, err
	}
	for _, la := range loginAgents() {
		if la.id == id {
			return la, nil
		}
	}
	return loginAgent{}, fmt.Errorf("unknown agent %q", name)
}

func loginPath() string {
	return filepath.Join(filepath.Dir(Path()), "accounts", "accounts.json")
}

func loadLogins() loginFile {
	var f loginFile
	if b, err := os.ReadFile(loginPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func saveLogins(f loginFile) error {
	p := loginPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
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

func sameLogin(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func findLogin(f loginFile, agent, user string) int {
	for i, r := range f.Accounts {
		if r.Agent == agent && sameLogin(r.User, user) {
			return i
		}
	}
	return -1
}

// track records the account an agent is signed in as. A new account is on
// when another account of that agent is already remembered: the second and
// later ones run alongside the first. creds is what to keep in the store (nil
// while the agent holds the tokens). It returns a pointer into f, valid until
// f changes.
func track(f *loginFile, la loginAgent, user, plan string, creds json.RawMessage) *loginRecord {
	i := findLogin(*f, la.id, user)
	if i < 0 {
		on := false
		for _, r := range f.Accounts {
			if r.Agent == la.id {
				on = true
				break
			}
		}
		f.Accounts = append(f.Accounts, loginRecord{Agent: la.id, User: user, On: on})
		i = len(f.Accounts) - 1
	}
	r := &f.Accounts[i]
	if plan != "" {
		r.Plan = plan
	}
	r.Creds = creds
	return r
}

// RememberLogins records the account each agent is signed in as. It runs
// before anything that replaces an agent's login, so the account that is
// signed in now is never lost.
func RememberLogins() error {
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	seen := false
	for _, la := range loginAgents() {
		if user, plan, _, ok := la.signedIn(); ok {
			track(&f, la, user, plan, nil)
			seen = true
		}
	}
	if !seen {
		return nil
	}
	return saveLogins(f)
}

// Logins lists the accounts remembered for an agent, the signed-in one first.
func Logins(agent string) ([]Login, error) {
	la, err := loginAgentFor(agent)
	if err != nil {
		return nil, err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	cur, plan, _, signed := la.signedIn()
	if signed {
		track(&f, la, cur, plan, nil)
		if err := saveLogins(f); err != nil {
			return nil, err
		}
	}
	var out []Login
	for pass := 0; pass < 2; pass++ {
		for _, r := range f.Accounts {
			if r.Agent != la.id {
				continue
			}
			current := signed && sameLogin(r.User, cur)
			if (pass == 0) != current {
				continue
			}
			out = append(out, Login{Agent: la.id, User: r.User, Plan: r.Plan, Current: current, On: current || r.On})
		}
	}
	return out, nil
}

// SwitchLogin signs the agent in as a remembered account. The account it was
// signed in as is remembered with its latest tokens and takes over the "on"
// flag the target had. It returns the account's stored name.
func SwitchLogin(agent, user string) (string, error) {
	la, err := loginAgentFor(agent)
	if err != nil {
		return "", err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	cur, curPlan, curCreds, signed := la.signedIn()
	if signed && sameLogin(cur, user) {
		i := findLogin(f, la.id, cur)
		if i < 0 {
			return cur, nil
		}
		return f.Accounts[i].User, nil
	}
	i := findLogin(f, la.id, user)
	if i < 0 {
		return "", fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", la.name, user, la.id)
	}
	if f.Accounts[i].Creds == nil {
		return "", fmt.Errorf("magpie has no sign-in for %s any more; sign in to it with magpie accounts add %s", f.Accounts[i].User, la.id)
	}
	target := f.Accounts[i].User
	creds := f.Accounts[i].Creds
	if signed {
		// park the outgoing account's latest tokens before the agent's are replaced
		track(&f, la, cur, curPlan, curCreds)
	}
	if err := la.sign(creds); err != nil {
		return "", err
	}
	i = findLogin(f, la.id, target)
	if signed {
		// the outgoing account takes over the "on" flag the target had
		o := findLogin(f, la.id, cur)
		f.Accounts[o].On = f.Accounts[i].On
	}
	f.Accounts[i].On = true
	f.Accounts[i].Creds = nil
	if err := saveLogins(f); err != nil {
		return "", err
	}
	return target, nil
}

// ForgetLogin drops a remembered account. The one the agent is signed in as
// cannot be forgotten. It returns the account's stored name.
func ForgetLogin(agent, user string) (string, error) {
	la, err := loginAgentFor(agent)
	if err != nil {
		return "", err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	cur, _, _, signed := la.signedIn()
	if signed && sameLogin(cur, user) {
		return "", fmt.Errorf("%s is signed in as %s; switch to another account before forgetting it", la.name, cur)
	}
	i := findLogin(f, la.id, user)
	if i < 0 {
		return "", fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", la.name, user, la.id)
	}
	name := f.Accounts[i].User
	f.Accounts = append(f.Accounts[:i], f.Accounts[i+1:]...)
	return name, saveLogins(f)
}

// SetLoginOn makes a remembered account used alongside the signed-in one, or
// stops that. The account the agent is signed in as is always on.
func SetLoginOn(agent, user string, on bool) error {
	la, err := loginAgentFor(agent)
	if err != nil {
		return err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	cur, plan, _, signed := la.signedIn()
	if signed {
		track(&f, la, cur, plan, nil)
	}
	i := findLogin(f, la.id, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", la.name, user, la.id)
	}
	if !on && signed && sameLogin(cur, user) {
		return fmt.Errorf("%s is signed in as %s, which is always on", la.name, cur)
	}
	f.Accounts[i].On = on
	return saveLogins(f)
}

// AddLogin runs the agent's own sign-in, which opens the vendor's login page,
// and remembers the account it ends up with. The account signed in before is
// remembered first, so it is not lost when the new sign-in replaces it. It
// returns the account signed in afterwards.
func AddLogin(agent string) (string, error) {
	la, err := loginAgentFor(agent)
	if err != nil {
		return "", err
	}
	var bin string
	var args []string
	switch la.id {
	case "claude":
		bin, args = claudeExecutable(), []string{"auth", "login"}
	case "codex":
		bin, args = "", []string{"login"}
		if p, err := exec.LookPath("codex"); err == nil {
			bin = p
		}
	}
	if bin == "" {
		return "", fmt.Errorf("%s is not installed on this machine", la.name)
	}
	loginMu.Lock()
	if user, plan, creds, ok := la.signedIn(); ok {
		f := loadLogins()
		track(&f, la, user, plan, creds)
		if err := saveLogins(f); err != nil {
			loginMu.Unlock()
			return "", err
		}
	}
	loginMu.Unlock()
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s sign-in did not finish: %v", la.name, err)
	}
	if err := RememberLogins(); err != nil {
		return "", err
	}
	user, _, _, ok := la.signedIn()
	if !ok {
		return "", errors.New(la.name + " is still signed out")
	}
	return user, nil
}

// ---- Claude Code ---------------------------------------------------------------

// claudeLocation is where Claude Code keeps the credentials when it is signed out too.
func claudeLocation() claudeCredentialLocation {
	path := filepath.Join(claudeConfigDir(), ".credentials.json")
	if _, err := os.Stat(path); err == nil || !claudeKeychain {
		return claudeCredentialLocation{path: path}
	}
	return claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
}

// claudeRawAt reads the whole credential blob at loc, empty if there is none.
func claudeRawAt(loc claudeCredentialLocation) map[string]any {
	var b []byte
	if loc.keychain {
		b, _ = exec.Command("security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	} else {
		b, _ = os.ReadFile(loc.path)
	}
	var raw map[string]any
	if json.Unmarshal(bytes.TrimSpace(b), &raw) != nil || raw == nil {
		raw = map[string]any{}
	}
	return raw
}

func claudeSignedIn() (string, string, json.RawMessage, bool) {
	forgetClaudeCredential()
	forgetClaudeStatus()
	c, _, ok := readClaudeCredential()
	if !ok {
		return "", "", nil, false
	}
	user, statusPlan, signedOut := claudeWho()
	if signedOut {
		return "", "", nil, false
	}
	plan := c.OAuth.SubscriptionType
	if statusPlan != "" {
		plan = statusPlan
	}
	if user == "" {
		user = "Claude account"
	}
	b, err := c.marshal()
	if err != nil {
		return "", "", nil, false
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return "", "", nil, false
	}
	return user, plan, raw["claudeAiOauth"], true
}

func claudeSign(creds json.RawMessage) error {
	var auth claudeAuth
	var oauth map[string]any
	if json.Unmarshal(creds, &auth) != nil || json.Unmarshal(creds, &oauth) != nil {
		return errors.New("the saved Claude Code sign-in is unreadable")
	}
	loc := claudeLocation()
	raw := claudeRawAt(loc)
	raw["claudeAiOauth"] = oauth
	if !loc.keychain {
		if err := os.MkdirAll(filepath.Dir(loc.path), 0o700); err != nil {
			return err
		}
	}
	err := saveClaudeCredential(loc, claudeCredentials{raw: raw, OAuth: auth})
	forgetClaudeCredential()
	forgetClaudeStatus()
	return err
}

// ---- Codex -----------------------------------------------------------------------

// codexLoginKeys are the fields of ~/.codex/auth.json that make up a sign-in.
var codexLoginKeys = []string{"auth_mode", "tokens", "last_refresh"}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// codexRaw reads auth.json as it is: empty when missing, an error when it is not JSON.
func codexRaw(path string) (map[string]json.RawMessage, error) {
	raw := map[string]json.RawMessage{}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %v", path, err)
	}
	return raw, nil
}

func codexSignedIn() (string, string, json.RawMessage, bool) {
	path := codexAuthPath()
	var a codexAuth
	if !readJSON(path, &a) || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
		return "", "", nil, false
	}
	id := jwtClaims(a.Tokens.IDToken)
	user := claimString(id, "email")
	if user == "" {
		user = "ChatGPT"
	}
	plan := claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")
	raw, err := codexRaw(path)
	if err != nil {
		return "", "", nil, false
	}
	creds := map[string]json.RawMessage{}
	for _, k := range codexLoginKeys {
		if v, ok := raw[k]; ok {
			creds[k] = v
		}
	}
	b, err := json.Marshal(creds)
	if err != nil {
		return "", "", nil, false
	}
	return user, plan, b, true
}

func codexSign(creds json.RawMessage) error {
	var in map[string]json.RawMessage
	if json.Unmarshal(creds, &in) != nil {
		return errors.New("the saved Codex sign-in is unreadable")
	}
	path := codexAuthPath()
	raw, err := codexRaw(path)
	if err != nil {
		return err
	}
	for _, k := range codexLoginKeys {
		if v, ok := in[k]; ok {
			raw[k] = v
		}
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}
