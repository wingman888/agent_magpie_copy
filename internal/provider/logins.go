package provider

// Saved logins: the Claude Code and Codex sign-ins magpie has seen, kept so
// the user can switch between them and run several at once.
//
// Each login's refresh token has exactly one holder. The login the agent is
// signed in to stays in the agent's own store, where the agent refreshes it.
// Every other saved login keeps its tokens in a file magpie owns, under
// accounts/<agent>/<tag>/, readable by the user only. Switching moves tokens
// between the two places; nothing else in the agent's files changes.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// savedLogin is one entry of accounts.json. Its tokens are not in it.
type savedLogin struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	// On keeps the login among the gateway's candidates while the agent is
	// signed in to another one. The login the agent is signed in to is always
	// on, whatever this says.
	On bool `json:"on,omitempty"`
}

type loginFile struct {
	Logins []savedLogin `json:"logins"`
}

// Login is a saved login as the user sees it.
type Login struct {
	Agent   string
	User    string
	Plan    string
	Current bool // the agent is signed in to it
	On      bool // used by the gateway alongside the current login
}

// codexLoginFields are the keys of ~/.codex/auth.json that hold a sign-in;
// the rest of the file belongs to Codex CLI and is left alone.
var codexLoginFields = []string{"auth_mode", "tokens", "last_refresh"}

// agentLogin runs the agent's own sign-in, which opens the vendor's login page
// and returns once the user has signed in. A var so tests can stand in for it.
var agentLogin = func(agent string) error {
	bin, args := "claude", []string{"auth", "login"}
	if agent == "codex" {
		bin, args = "codex", []string{"login"}
	} else if p := claudeExecutable(); p != "" {
		bin = p
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// LoginAgent names the agent a sign-in command means: "claude" (or "cc") or
// "codex". ok is false for any other name.
func LoginAgent(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func unknownAgent(name string) error {
	return fmt.Errorf("unknown agent %q: sign-ins are kept for claude (cc) and codex", name)
}

func loginsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

// loginDir is where magpie keeps a saved login's tokens.
func loginDir(agent, user string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(user))))
	return filepath.Join(filepath.Dir(Path()), "accounts", agent, hex.EncodeToString(sum[:8]))
}

// loginTokens is the file holding a saved login's tokens, named as the agent
// names its own, so Claude Code can read a login kept in loginDir.
func loginTokens(agent, user string) string {
	name := "auth.json"
	if agent == "claude" {
		name = ".credentials.json"
	}
	return filepath.Join(loginDir(agent, user), name)
}

// codexAuthPath is the file Codex CLI keeps its sign-in in.
func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

func loadLogins() []savedLogin {
	var f loginFile
	if b, err := os.ReadFile(loginsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f.Logins
}

func storeLogins(ls []savedLogin) error {
	return writePrivate(loginsPath(), loginFile{Logins: ls})
}

// writePrivate writes a JSON file readable by the user only.
func writePrivate(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func findLogin(ls []savedLogin, agent, user string) int {
	user = strings.TrimSpace(user)
	for i, l := range ls {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			return i
		}
	}
	return -1
}

// currentLogin is the login the agent is signed in to.
func currentLogin(agent string) (Provider, bool) {
	if agent == "claude" {
		return claudeSelf()
	}
	home, _ := os.UserHomeDir()
	return codexSelf(home)
}

// currentUser is who the agent is signed in as, or "" when it is signed out.
func currentUser(agent string) string {
	if p, ok := currentLogin(agent); ok && p.Account != nil {
		return p.Account.User
	}
	return ""
}

// isCurrent is true when user is the login the agent is signed in to.
func isCurrent(agent, user string) bool {
	cur := currentUser(agent)
	return cur != "" && strings.EqualFold(cur, strings.TrimSpace(user))
}

// recordLogin remembers a login. One seen for the first time gets on; one
// already remembered keeps its on flag and takes a newer plan.
func recordLogin(agent, user, plan string, on bool) error {
	ls := loadLogins()
	if i := findLogin(ls, agent, user); i >= 0 {
		if plan == "" || ls[i].Plan == plan {
			return nil
		}
		ls[i].Plan = plan
		return storeLogins(ls)
	}
	ls = append(ls, savedLogin{Agent: agent, User: strings.TrimSpace(user), Plan: plan, On: on})
	return storeLogins(ls)
}

// rememberCurrent records the login each agent is signed in to, so it can be
// switched back to later. The tokens stay where the agent keeps them.
func rememberCurrent() {
	for _, agent := range []string{"claude", "codex"} {
		if p, ok := currentLogin(agent); ok && p.Account != nil && p.Account.User != "" {
			recordLogin(agent, p.Account.User, p.Account.Plan, false)
		}
	}
}

// savedProvider is a saved login as a provider that can take requests.
func savedProvider(l savedLogin) (Provider, bool) {
	switch l.Agent {
	case "claude":
		return claudeProvider(loginDir(l.Agent, l.User), l.User, l.Plan), true
	case "codex":
		return codexProvider(loginTokens(l.Agent, l.User), l.User, l.Plan), true
	}
	return Provider{}, false
}

// withAlternates attaches the saved logins that are on at the same time to
// the login the agent is signed in to. They are tried after it, in the order
// they were saved. A login whose tokens are missing is left out.
func withAlternates(cur Provider) Provider {
	acct := *cur.Account
	acct.also = nil
	for _, l := range loadLogins() {
		if l.Agent != acct.Agent || !l.On || strings.EqualFold(l.User, acct.User) {
			continue
		}
		if _, err := os.Stat(loginTokens(l.Agent, l.User)); err != nil {
			continue
		}
		if p, ok := savedProvider(l); ok {
			acct.also = append(acct.also, p)
		}
	}
	cur.Account = &acct
	return cur
}

// Logins lists the saved logins of one agent, or of both when agent is "".
// Within an agent the login the agent is signed in to comes first.
func Logins(agent string) []Login {
	rememberCurrent()
	agents := []string{"claude", "codex"}
	if a, ok := LoginAgent(agent); ok {
		agents = []string{a}
	}
	ls := loadLogins()
	var out []Login
	for _, a := range agents {
		signedIn := currentUser(a)
		var first, rest []Login
		for _, l := range ls {
			if l.Agent != a {
				continue
			}
			isCur := signedIn != "" && strings.EqualFold(signedIn, l.User)
			x := Login{Agent: a, User: l.User, Plan: l.Plan, Current: isCur, On: isCur || l.On}
			if isCur {
				first = append(first, x)
			} else {
				rest = append(rest, x)
			}
		}
		out = append(out, first...)
		out = append(out, rest...)
	}
	return out
}

// saveOutgoing keeps the login the agent is signed in to in magpie's own
// store, with the newest tokens the agent has.
func saveOutgoing(agent, user string) error {
	if err := os.MkdirAll(loginDir(agent, user), 0o700); err != nil {
		return err
	}
	if agent == "claude" {
		c, _, ok := readClaudeCredential()
		if !ok {
			return errors.New("Claude Code is signed out")
		}
		out := claudeCredentials{raw: map[string]any{"claudeAiOauth": c.raw["claudeAiOauth"]}, OAuth: c.OAuth}
		return writeClaudeCredential(claudeCredentialLocation{path: loginTokens(agent, user)}, out)
	}
	var raw map[string]any
	if !readJSON(codexAuthPath(), &raw) {
		return errors.New("Codex is signed out")
	}
	out := map[string]any{}
	for _, k := range codexLoginFields {
		if v, ok := raw[k]; ok {
			out[k] = v
		}
	}
	return writePrivate(loginTokens(agent, user), out)
}

// claudeDefaultLocation is where Claude Code keeps its login when it has none.
func claudeDefaultLocation() claudeCredentialLocation {
	if claudeKeychain {
		return claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
	}
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return claudeCredentialLocation{path: filepath.Join(dir, ".credentials.json")}
}

// takeIn makes a saved login the one the agent is signed in to. Only the
// sign-in fields of the agent's own file change; everything else stays.
func takeIn(agent string, l savedLogin) error {
	b, err := os.ReadFile(loginTokens(agent, l.User))
	if err != nil {
		return fmt.Errorf("no saved sign-in for %s: %v", l.User, err)
	}
	if agent == "claude" {
		in, ok := parseClaudeCredentials(b)
		if !ok {
			return errors.New("the saved Claude Code sign-in is unreadable")
		}
		cur, loc, ok := readClaudeCredential()
		if !ok {
			loc = claudeDefaultLocation()
			cur = claudeCredentials{}
			if !loc.keychain {
				if old, err := os.ReadFile(loc.path); err == nil && json.Unmarshal(old, &cur.raw) != nil {
					return errors.New("Claude Code's credentials file is unreadable; not overwriting it")
				}
			}
		}
		if cur.raw == nil {
			cur.raw = map[string]any{}
		}
		cur.raw["claudeAiOauth"] = in.raw["claudeAiOauth"]
		cur.OAuth = in.OAuth
		return saveClaudeCredential(loc, cur)
	}
	var in map[string]any
	if json.Unmarshal(b, &in) != nil {
		return errors.New("the saved Codex sign-in is unreadable")
	}
	raw := map[string]any{}
	path := codexAuthPath()
	if old, err := os.ReadFile(path); err == nil && json.Unmarshal(old, &raw) != nil {
		return errors.New("Codex's auth file is unreadable; not overwriting it")
	}
	if raw == nil {
		raw = map[string]any{}
	}
	for _, k := range codexLoginFields {
		if v, ok := in[k]; ok {
			raw[k] = v
		}
	}
	return writePrivate(path, raw)
}

// SwitchLogin signs the agent in to a saved login. The login it was signed in
// to is saved with its newest tokens and takes the on flag of the login it
// gives way to. It reports false when that login was already the current one.
func SwitchLogin(agent, user string) (bool, error) {
	a, ok := LoginAgent(agent)
	if !ok {
		return false, unknownAgent(agent)
	}
	rememberCurrent()
	ls := loadLogins()
	i := findLogin(ls, a, user)
	if i < 0 {
		return false, fmt.Errorf("no saved %s login %q; magpie accounts %s lists them", a, user, a)
	}
	target := ls[i]
	outgoing := currentUser(a)
	if outgoing != "" && strings.EqualFold(outgoing, target.User) {
		return false, nil
	}
	if outgoing != "" {
		if err := saveOutgoing(a, outgoing); err != nil {
			return false, err
		}
		if j := findLogin(ls, a, outgoing); j >= 0 {
			ls[j].On = target.On
		}
	}
	if err := takeIn(a, target); err != nil {
		if outgoing != "" {
			// the agent still holds the outgoing login; drop the copy
			os.RemoveAll(loginDir(a, outgoing))
		}
		return false, err
	}
	os.RemoveAll(loginDir(a, target.User))
	forgetClaudeCredential()
	forgetClaudeStatus()
	return true, storeLogins(ls)
}

// ForgetLogin drops a saved login and its tokens. The login the agent is
// signed in to cannot be forgotten.
func ForgetLogin(agent, user string) error {
	a, ok := LoginAgent(agent)
	if !ok {
		return unknownAgent(agent)
	}
	rememberCurrent()
	ls := loadLogins()
	i := findLogin(ls, a, user)
	if i < 0 {
		return fmt.Errorf("no saved %s login %q; magpie accounts %s lists them", a, user, a)
	}
	if isCurrent(a, ls[i].User) {
		return fmt.Errorf("%s is the login %s is signed in to; switch to another login first, then forget it", ls[i].User, a)
	}
	os.RemoveAll(loginDir(a, ls[i].User))
	ls = append(ls[:i], ls[i+1:]...)
	return storeLogins(ls)
}

// AddLogin runs the agent's own sign-in and saves the login it leaves behind.
// The login the agent had is kept too. A login added while another is saved
// is on at the same time. When the sign-in does not finish, the earlier login
// is put back. It returns the user the agent is now signed in as.
func AddLogin(agent string) (string, error) {
	a, ok := LoginAgent(agent)
	if !ok {
		return "", unknownAgent(agent)
	}
	rememberCurrent()
	prev := currentUser(a)
	if prev != "" {
		if err := saveOutgoing(a, prev); err != nil {
			return "", err
		}
	}
	loginErr := agentLogin(a)
	forgetClaudeCredential()
	forgetClaudeStatus()
	now, signedIn := currentLogin(a)
	nowUser := ""
	if signedIn && now.Account != nil {
		nowUser = now.Account.User
	}
	if nowUser == "" {
		if prev != "" {
			// the sign-in did not finish: the earlier login goes back
			ls := loadLogins()
			if j := findLogin(ls, a, prev); j >= 0 {
				if err := takeIn(a, ls[j]); err != nil {
					return "", err
				}
			}
			os.RemoveAll(loginDir(a, prev))
			forgetClaudeCredential()
			forgetClaudeStatus()
		}
		if loginErr != nil {
			return "", loginErr
		}
		return "", fmt.Errorf("the %s sign-in did not finish", a)
	}
	// the new login is the agent's now, so its tokens have one holder
	os.RemoveAll(loginDir(a, nowUser))
	ls := loadLogins()
	if j := findLogin(ls, a, nowUser); j >= 0 {
		if now.Account.Plan != "" {
			ls[j].Plan = now.Account.Plan
		}
	} else {
		others := 0
		for _, l := range ls {
			if l.Agent == a {
				others++
			}
		}
		ls = append(ls, savedLogin{Agent: a, User: nowUser, Plan: now.Account.Plan, On: others > 0})
	}
	if err := storeLogins(ls); err != nil {
		return "", err
	}
	return nowUser, nil
}

// SetLoginOn makes a saved login one the gateway uses alongside the login the
// agent is signed in to, or stops it. The signed-in login is always on.
func SetLoginOn(agent, user string, on bool) error {
	a, ok := LoginAgent(agent)
	if !ok {
		return unknownAgent(agent)
	}
	rememberCurrent()
	ls := loadLogins()
	i := findLogin(ls, a, user)
	if i < 0 {
		return fmt.Errorf("no saved %s login %q; magpie accounts %s lists them", a, user, a)
	}
	if !on && isCurrent(a, ls[i].User) {
		return fmt.Errorf("%s is the login %s is signed in to, so it stays on", ls[i].User, a)
	}
	ls[i].On = on
	return storeLogins(ls)
}
