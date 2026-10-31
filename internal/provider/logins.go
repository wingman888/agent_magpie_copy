package provider

// Several subscriptions per agent. magpie keeps following each agent's own
// sign-in; it also remembers every Codex and Claude Code account it has
// seen signed in, so the user can switch back to one without signing in
// again. A switch moves the saved credentials into the agent's own store
// and saves the ones they replace, so each account's refresh token lives in
// exactly one place: the vendor rotates it on every refresh, and two holders
// of the same one would sign each other out.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Login is a remembered subscription account, without its secrets.
type Login struct {
	Agent  string    `json:"agent"`
	User   string    `json:"user"`
	Plan   string    `json:"plan,omitempty"`
	Seen   time.Time `json:"seen"`
	Active bool      `json:"active"` // the agent is signed in to this one now
	On     bool      `json:"on"`     // in use: the active one, or next in line
}

type savedLogin struct {
	Agent string    `json:"agent"`
	User  string    `json:"user"`
	Plan  string    `json:"plan,omitempty"`
	Seen  time.Time `json:"seen"`
	// On puts the account in use beside the one the agent is signed in to:
	// requests go to it when that one is out of quota (see logins_on.go).
	On bool `json:"on,omitempty"`
	// Auth is the agent's credential blob as the agent stores it: Codex's
	// auth.json, Claude Code's keychain item / .credentials.json.
	Auth json.RawMessage `json:"auth"`
	// Profile is Claude Code's oauthAccount from .claude.json, which says
	// whose credentials those are.
	Profile json.RawMessage `json:"profile,omitempty"`
	// Home is where a Grok account magpie signed in keeps its sign-in; the
	// Grok CLI's own account has none (see grok_accounts.go). First puts a
	// Grok or Copilot account ahead of the agent's own (side_logins.go).
	Home  string `json:"home,omitempty"`
	First bool   `json:"first,omitempty"`
}

var (
	loginsMu     sync.Mutex
	loginsSeenAt time.Time
)

// switchable agents: those whose sign-in magpie can save and put back.
var loginAgents = []string{"claude", "codex"}

func loginsPath() string { return filepath.Join(filepath.Dir(Path()), "logins.json") }

func readLogins() []savedLogin {
	var out []savedLogin
	b, err := os.ReadFile(loginsPath())
	if err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

func writeLogins(ls []savedLogin) error {
	sort.SliceStable(ls, func(i, j int) bool {
		if ls[i].Agent != ls[j].Agent {
			return ls[i].Agent < ls[j].Agent
		}
		return strings.ToLower(ls[i].User) < strings.ToLower(ls[j].User)
	})
	b, err := json.MarshalIndent(ls, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(loginsPath(), append(b, '\n'))
}

// writePrivate replaces a file readable by the user alone, atomically, so
// an agent reading it at that moment sees either version, never half.
func writePrivate(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func upsertLogin(ls []savedLogin, l savedLogin) []savedLogin {
	for i := range ls {
		if sameLogin(ls[i], l) {
			l.On = l.On || ls[i].On
			ls[i] = l
			return ls
		}
	}
	return append(ls, l)
}

// sameLogin says whether two saved logins are one account. A Claude
// account is its email within an organization: one email can be a personal
// Pro or Max and a seat on a Team, two subscriptions side by side.
func sameLogin(a, b savedLogin) bool {
	if a.Agent != b.Agent {
		return false
	}
	if a.Agent == "claude" {
		ea, oa := claudeWho(a.Profile)
		eb, ob := claudeWho(b.Profile)
		if ea != "" && eb != "" && oa != "" && ob != "" {
			return strings.EqualFold(ea, eb) && oa == ob
		}
	}
	return strings.EqualFold(a.User, b.User)
}

// claudeWho reads the email and organization of Claude Code's oauthAccount.
func claudeWho(profile json.RawMessage) (email, org string) {
	var acct struct {
		Email string `json:"emailAddress"`
		Org   string `json:"organizationUuid"`
	}
	if json.Unmarshal(profile, &acct) != nil {
		return "", ""
	}
	return acct.Email, acct.Org
}

// claudeUser names a Claude account: its email, and for a seat on a Team
// or Enterprise the organization too, so it reads apart from a personal
// subscription of the same email.
func claudeUser(email, plan string, acct map[string]any) string {
	if email == "" || (plan != "team" && plan != "enterprise") {
		return email
	}
	org, _ := acct["organizationName"].(string)
	if org = strings.TrimSpace(org); org == "" || strings.Contains(org, email) {
		org = strings.ToUpper(plan[:1]) + plan[1:]
	}
	return email + " · " + org
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// claudeProfilePath is Claude Code's global state file, which holds the
// signed-in account's identity next to much else.
func claudeProfilePath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

func readClaudeProfile() (map[string]any, error) {
	b, err := os.ReadFile(claudeProfilePath())
	if err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber() // numbers go back exactly as they came
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		return nil, err
	}
	return m, nil
}

// liveLogin reads the account an agent is signed in to now.
func liveLogin(agent string) (savedLogin, bool) {
	switch agent {
	case "codex":
		b, err := os.ReadFile(codexAuthPath())
		if err != nil {
			return savedLogin{}, false
		}
		var a codexAuth
		if json.Unmarshal(b, &a) != nil || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return savedLogin{}, false
		}
		id := jwtClaims(a.Tokens.IDToken)
		user := claimString(id, "email")
		if user == "" {
			user = a.Tokens.AccountID
		}
		if user == "" {
			return savedLogin{}, false
		}
		return savedLogin{Agent: agent, User: user, Plan: claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"),
			Auth: json.RawMessage(bytes.TrimSpace(b))}, true
	case "claude":
		c, _, ok := claudeCredential()
		if !ok {
			return savedLogin{}, false
		}
		b, err := c.marshal()
		if err != nil {
			return savedLogin{}, false
		}
		// credentials a logout left behind are not a sign-in: Claude Code
		// says so, and the account is not a provider either (claudeAccount)
		user, _, signedOut := claudeIdentity()
		if signedOut {
			return savedLogin{}, false
		}
		l := savedLogin{Agent: agent, Plan: c.OAuth.SubscriptionType, Auth: b}
		if m, err := readClaudeProfile(); err == nil {
			if acct, ok := m["oauthAccount"].(map[string]any); ok {
				email, _ := acct["emailAddress"].(string)
				l.User = claudeUser(email, l.Plan, acct)
				l.Profile, _ = json.Marshal(acct)
			}
		}
		if l.User == "" {
			l.User = user
		}
		if l.User == "" {
			return savedLogin{}, false
		}
		return l, true
	}
	return savedLogin{}, false
}

// rememberLogins saves the accounts the agents are signed in to now. The
// copy of an active account is only a bookmark: the agent keeps refreshing
// its own, and a switch saves that fresher one first.
func rememberLogins(force bool) {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if !force && time.Since(loginsSeenAt) < 30*time.Second {
		return
	}
	loginsSeenAt = time.Now()
	ls := readLogins()
	changed := false
	for _, agent := range loginAgents {
		l, ok := liveLogin(agent)
		if !ok {
			continue
		}
		l.Seen = time.Now().UTC().Truncate(time.Second)
		ls = upsertLogin(ls, l)
		changed = true
	}
	if changed {
		_ = writeLogins(ls)
	}
}

// Logins lists the remembered accounts of an agent ("" for every one),
// the active one flagged.
func Logins(agent string) []Login {
	var side []Login
	switch agent {
	case "grok":
		return grokLoginList()
	case "copilot":
		return copilotLoginList()
	case "":
		side = append(grokLoginList(), copilotLoginList()...)
	}
	rememberLogins(false)
	loginsMu.Lock()
	defer loginsMu.Unlock()
	active := map[string]string{}
	for _, a := range loginAgents {
		if l, ok := liveLogin(a); ok {
			active[a] = l.User
		}
	}
	var out []Login
	for _, l := range readLogins() {
		if (agent != "" && l.Agent != agent) || l.Agent == "grok" || l.Agent == "copilot" {
			continue
		}
		using := strings.EqualFold(active[l.Agent], l.User)
		out = append(out, Login{Agent: l.Agent, User: l.User, Plan: l.Plan, Seen: l.Seen,
			Active: using, On: using || l.On})
	}
	return append(out, side...)
}

// SwitchLogin signs an agent in to a remembered account. Sessions of the
// agent that are already running keep the account they started with until
// they restart.
func SwitchLogin(agent, user string) error {
	switch agent {
	case "grok":
		return switchGrokLogin(user)
	case "copilot":
		return switchCopilotLogin(user)
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	var target *savedLogin
	for i := range ls {
		if ls[i].Agent == agent && strings.EqualFold(ls[i].User, user) {
			target = &ls[i]
		}
	}
	if target == nil {
		return fmt.Errorf("no saved %s account %q", agent, user)
	}
	want := *target
	if live, ok := liveLogin(agent); ok {
		if strings.EqualFold(live.User, want.User) {
			return nil
		}
		// the credentials being replaced, as fresh as the agent has them;
		// in use still if the one taking over was: it is next in line now
		live.Seen = time.Now().UTC().Truncate(time.Second)
		ls = upsertLogin(ls, live)
		for i := range ls {
			if ls[i].Agent == agent && strings.EqualFold(ls[i].User, live.User) {
				ls[i].On = want.On
			}
		}
		if err := writeLogins(ls); err != nil {
			return err
		}
	}
	var err error
	switch agent {
	case "codex":
		err = writePrivate(codexAuthPath(), append(bytes.TrimSpace(want.Auth), '\n'))
	case "claude":
		err = putClaudeLogin(want)
	default:
		err = fmt.Errorf("%s accounts can't be switched", agent)
	}
	if err != nil {
		return err
	}
	loginsSeenAt = time.Time{}
	forgetAccountCaches()
	return nil
}

func putClaudeLogin(l savedLogin) error {
	c, ok := parseClaudeCredentials(l.Auth)
	if !ok {
		return errors.New("the saved Claude Code sign-in is unreadable")
	}
	_, loc, found := readClaudeCredential()
	if !found {
		// signed out: put it where Claude Code keeps it on this system
		dir := os.Getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".claude")
		}
		loc = claudeCredentialLocation{path: filepath.Join(dir, ".credentials.json")}
		if claudeKeychain {
			loc = claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
		}
	}
	if err := saveClaudeCredential(loc, c); err != nil {
		return err
	}
	if len(l.Profile) == 0 {
		return nil
	}
	m, err := readClaudeProfile()
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		m = map[string]any{}
	}
	d := json.NewDecoder(bytes.NewReader(l.Profile))
	d.UseNumber()
	var acct map[string]any
	if err := d.Decode(&acct); err != nil {
		return err
	}
	m["oauthAccount"] = acct
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(claudeProfilePath(), append(b, '\n'))
}

// ForgetLogin drops a remembered account. The one an agent is signed in to
// now can't be forgotten; it would only be remembered again.
func ForgetLogin(agent, user string) error {
	switch agent {
	case "grok":
		return forgetGrokLogin(user)
	case "copilot":
		return forgetCopilotLogin(user)
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	if live, ok := liveLogin(agent); ok && strings.EqualFold(live.User, user) {
		return fmt.Errorf("%s is signed in to %s now; switch to another account first", agent, user)
	}
	ls := readLogins()
	out := ls[:0]
	found := false
	for _, l := range ls {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			found = true
			continue
		}
		out = append(out, l)
	}
	if !found {
		return fmt.Errorf("no saved %s account %q", agent, user)
	}
	return writeLogins(out)
}

// forgetAccountCaches makes the next look at the accounts read them afresh.
func forgetAccountCaches() {
	forgetClaudeCredential()
	forgetClaudeStatus()
	forgetCursorStatus()
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.at = time.Time{}
	subscriptionUsageCache.data = nil
	subscriptionUsageCache.Unlock()
}
