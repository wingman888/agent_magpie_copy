package provider

// Remembered logins: the sign-ins of Claude Code and Codex magpie keeps, so the
// user can switch between them, or let several serve the gateway at once.
//
// An agent holds one login at a time, in its own files. magpie keeps a copy of
// every account it has seen in accounts.json next to providers.json (mode 0600).
// Refresh tokens have one holder at a time: while an account is the agent's
// current login, the agent's files are the only copy that refreshes, and the
// copy magpie keeps is a snapshot replaced on every look. An account that is
// not current refreshes from its copy, and only there. An "on" account serves
// the gateway alongside the current one and never touches the agent's files.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// rememberedLogin is one signed-in account magpie keeps.
type rememberedLogin struct {
	Agent string         `json:"agent"`
	User  string         `json:"user"`
	Plan  string         `json:"plan,omitempty"`
	On    bool           `json:"on,omitempty"`  // serves the gateway alongside the current login
	Added time.Time      `json:"added"`
	Cred  map[string]any `json:"credential,omitempty"` // the sign-in as the agent keeps it, see codexLoginKeys
}

type loginsFile struct {
	Logins []rememberedLogin `json:"logins"`
}

// Login is a signed-in account magpie remembers.
type Login struct {
	Agent   string    `json:"agent"`
	User    string    `json:"user"`
	Plan    string    `json:"plan,omitempty"`
	Current bool      `json:"current"` // the agent is signed in as this one now
	On      bool      `json:"on"`      // serves the gateway alongside the current login; the current one always does
	Added   time.Time `json:"added"`
}

var loginsMu sync.Mutex

// codexLoginKeys are the auth.json fields that make up a Codex sign-in. The
// rest of the file is Codex CLI's own and is left alone.
var codexLoginKeys = []string{"auth_mode", "OPENAI_API_KEY", "tokens", "last_refresh"}

// loginCommand runs the agent's own sign-in, which opens the vendor's login
// page and returns when it is done. A var so tests never start an agent.
var loginCommand = func(agent string) error {
	var bin string
	var args []string
	switch agent {
	case "claude":
		bin, args = claudeExecutable(), []string{"auth", "login"}
	case "codex":
		if p, err := exec.LookPath("codex"); err == nil {
			bin = p
		}
		args = []string{"login"}
	}
	if bin == "" {
		return fmt.Errorf("%s is not installed", agentName(agent))
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func loginsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

func loadLogins() loginsFile {
	var f loginsFile
	if b, err := os.ReadFile(loginsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

// editLogins runs fn on the stored logins under a lock and saves them when fn
// reports a change. The file is written 0600: it holds refresh tokens.
func editLogins(fn func(f *loginsFile) (bool, error)) error {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	changed, err := fn(&f)
	if err != nil || !changed {
		return err
	}
	p := loginsPath()
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

// liveLogin is the sign-in the agent itself uses now.
type liveLogin struct {
	user, plan string // user is "" when the sign-in names no email
	cred       map[string]any
}

func knownAgent(agent string) bool { return agent == "claude" || agent == "codex" }

func agentName(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

func unknownAgent(agent string) error {
	return fmt.Errorf("unknown agent %q: magpie remembers claude (cc) and codex accounts", agent)
}

// currentLogin reads the sign-in the agent uses now. present is false when
// nobody is signed in.
func currentLogin(agent string) (live liveLogin, present bool) {
	switch agent {
	case "claude":
		return currentClaudeLogin()
	case "codex":
		return currentCodexLogin()
	}
	return liveLogin{}, false
}

func currentClaudeLogin() (liveLogin, bool) {
	c, _, ok := claudeCredential()
	if !ok {
		return liveLogin{}, false
	}
	user, plan, signedOut := claudeIdentity()
	if signedOut {
		return liveLogin{}, false
	}
	if email := claudeFileEmail(); email != "" {
		user = email
	}
	if plan == "" {
		plan = c.OAuth.SubscriptionType
	}
	cred := map[string]any{"claudeAiOauth": c.raw["claudeAiOauth"]}
	if oa := claudeFileAccount(); oa != nil {
		cred["oauthAccount"] = oa
	}
	return liveLogin{user: strings.TrimSpace(user), plan: plan, cred: cred}, true
}

func currentCodexLogin() (liveLogin, bool) {
	home, _ := os.UserHomeDir()
	raw, err := readJSONMap(codexAuthPath(home))
	if err != nil {
		return liveLogin{}, false
	}
	var a codexAuth
	b, _ := json.Marshal(raw)
	json.Unmarshal(b, &a)
	if a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
		return liveLogin{}, false
	}
	id := jwtClaims(a.Tokens.IDToken)
	cred := map[string]any{}
	for _, k := range codexLoginKeys {
		if v, ok := raw[k]; ok {
			cred[k] = v
		}
	}
	return liveLogin{user: claimString(id, "email"), plan: claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"), cred: cred}, true
}

func codexAuthPath(home string) string { return filepath.Join(home, ".codex", "auth.json") }

// claudeStateFile is Claude Code's settings-and-state file. oauthAccount in it
// names the account the credentials belong to.
func claudeStateFile() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

func claudeCredentialsFile() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".credentials.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", ".credentials.json")
}

// claudeFileAccount is the oauthAccount object Claude Code wrote at sign-in, or nil.
func claudeFileAccount() map[string]any {
	var raw map[string]any
	if !readJSON(claudeStateFile(), &raw) {
		return nil
	}
	oa, _ := raw["oauthAccount"].(map[string]any)
	return oa
}

// claudeFileEmail is the email of the account Claude Code signed in with, or "".
func claudeFileEmail() string {
	s, _ := claudeFileAccount()["emailAddress"].(string)
	return strings.TrimSpace(s)
}

// readJSONMap reads a JSON object; a missing file is an empty object. A file
// that is not a JSON object is an error, so nothing is overwritten by accident.
func readJSONMap(path string) (map[string]any, error) {
	raw := map[string]any{}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return raw, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s is not a JSON object; magpie leaves it alone", path)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	return raw, nil
}

func writeJSONMap(path string, raw map[string]any) error {
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// writeLogin puts a remembered sign-in into the agent's own files, changing
// only the fields that make up the sign-in.
func writeLogin(agent string, cred map[string]any) error {
	switch agent {
	case "claude":
		return writeClaudeLogin(cred)
	case "codex":
		return writeCodexLogin(cred)
	}
	return unknownAgent(agent)
}

func writeClaudeLogin(cred map[string]any) error {
	oauth := cred["claudeAiOauth"]
	if oauth == nil {
		return errors.New("the saved Claude Code sign-in is empty")
	}
	raw := map[string]any{}
	cur, loc, have := readClaudeCredential()
	if have && cur.raw != nil {
		raw = cur.raw
	} else {
		loc = claudeCredentialLocation{path: claudeCredentialsFile()}
	}
	raw["claudeAiOauth"] = oauth
	b, _ := json.Marshal(raw)
	c, ok := parseClaudeCredentials(b)
	if !ok {
		return errors.New("the saved Claude Code sign-in is not valid")
	}
	if err := saveClaudeCredential(loc, c); err != nil {
		return err
	}
	if oa := cred["oauthAccount"]; oa != nil {
		state, err := readJSONMap(claudeStateFile())
		if err != nil {
			return err
		}
		state["oauthAccount"] = oa
		if err := writeJSONMap(claudeStateFile(), state); err != nil {
			return err
		}
	}
	forgetClaudeCredential()
	forgetClaudeStatus()
	return nil
}

func writeCodexLogin(cred map[string]any) error {
	home, _ := os.UserHomeDir()
	path := codexAuthPath(home)
	raw, err := readJSONMap(path)
	if err != nil {
		return err
	}
	for _, k := range codexLoginKeys {
		delete(raw, k)
	}
	for _, k := range codexLoginKeys {
		if v, ok := cred[k]; ok {
			raw[k] = v
		}
	}
	return writeJSONMap(path, raw)
}

func sameUser(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	return a != "" && strings.EqualFold(a, b)
}

func sameJSON(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func findLogin(f *loginsFile, agent, user string) int {
	for i, l := range f.Logins {
		if l.Agent == agent && sameUser(l.User, user) {
			return i
		}
	}
	return -1
}

func countLogins(f *loginsFile, agent string) int {
	n := 0
	for _, l := range f.Logins {
		if l.Agent == agent {
			n++
		}
	}
	return n
}

func storedLogin(agent, user string) (rememberedLogin, bool) {
	f := loadLogins()
	i := findLogin(&f, agent, user)
	if i < 0 {
		return rememberedLogin{}, false
	}
	return f.Logins[i], true
}

// remember records the agent's current sign-in, or refreshes the copy kept of
// it. An account recorded while another one is already remembered is on by
// default: that is how the second account comes to serve alongside the first.
func remember(agent string) {
	live, ok := currentLogin(agent)
	if !ok || live.user == "" {
		return
	}
	editLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f, agent, live.user)
		if i < 0 {
			f.Logins = append(f.Logins, rememberedLogin{Agent: agent, User: live.user, Plan: live.plan,
				On: countLogins(f, agent) > 0, Added: time.Now(), Cred: live.cred})
			return true, nil
		}
		l := &f.Logins[i]
		if l.Plan == live.plan && sameJSON(l.Cred, live.cred) {
			return false, nil
		}
		l.Plan, l.Cred = live.plan, live.cred
		return true, nil
	})
}

// Logins lists the remembered accounts of an agent, the current one first, then
// in the order they were remembered. It first records the agent's sign-in.
func Logins(agent string) []Login {
	if !knownAgent(agent) {
		return nil
	}
	remember(agent)
	cur, present := currentLogin(agent)
	var out []Login
	for _, l := range loadLogins().Logins {
		if l.Agent != agent {
			continue
		}
		isCur := present && sameUser(cur.user, l.User)
		out = append(out, Login{Agent: agent, User: l.User, Plan: l.Plan, Current: isCur, On: l.On || isCur, Added: l.Added})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Current && !out[j].Current })
	return out
}

// SetLoginOn makes a remembered account serve the gateway alongside the current
// login, or stops it. The current login always serves, so it cannot be turned off.
func SetLoginOn(agent, user string, on bool) error {
	if !knownAgent(agent) {
		return unknownAgent(agent)
	}
	remember(agent)
	if cur, present := currentLogin(agent); !on && present && sameUser(cur.user, user) {
		return fmt.Errorf("%s is the account %s is signed in as now, and it always serves", agentName(agent), user)
	}
	return editLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f, agent, user)
		if i < 0 {
			return false, fmt.Errorf("no remembered %s account %q", agentName(agent), user)
		}
		if f.Logins[i].On == on {
			return false, nil
		}
		f.Logins[i].On = on
		return true, nil
	})
}

// SwitchLogin makes the agent sign in as a remembered account. The account it
// replaces is kept with its latest sign-in and takes over the on state of the
// account it is replaced by. Agent sessions already running keep the old login
// until they restart.
func SwitchLogin(agent, user string) error {
	if !knownAgent(agent) {
		return unknownAgent(agent)
	}
	remember(agent)
	live, present := currentLogin(agent)
	if present && live.user == "" {
		return fmt.Errorf("the %s sign-in in use names no email, so magpie cannot keep it; sign in again first", agentName(agent))
	}
	target, ok := storedLogin(agent, user)
	if !ok {
		return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agentName(agent), user, agent)
	}
	if present && sameUser(live.user, target.User) {
		return nil
	}
	if target.Cred == nil {
		return fmt.Errorf("magpie has no saved sign-in for %s; sign in as it again", target.User)
	}
	if err := writeLogin(agent, target.Cred); err != nil {
		return err
	}
	return editLogins(func(f *loginsFile) (bool, error) {
		on := false
		if t := findLogin(f, agent, target.User); t >= 0 {
			on = f.Logins[t].On
		}
		if present {
			if j := findLogin(f, agent, live.user); j >= 0 {
				f.Logins[j].Plan, f.Logins[j].Cred, f.Logins[j].On = live.plan, live.cred, on
			} else {
				f.Logins = append(f.Logins, rememberedLogin{Agent: agent, User: live.user, Plan: live.plan,
					On: on, Added: time.Now(), Cred: live.cred})
			}
		}
		return true, nil
	})
}

// ForgetLogin drops a remembered account. The one the agent is signed in as
// cannot be forgotten.
func ForgetLogin(agent, user string) error {
	if !knownAgent(agent) {
		return unknownAgent(agent)
	}
	remember(agent)
	if cur, present := currentLogin(agent); present && sameUser(cur.user, user) {
		return fmt.Errorf("%s is signed in as %s now; switch to another account before forgetting it", agentName(agent), user)
	}
	return editLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f, agent, user)
		if i < 0 {
			return false, fmt.Errorf("no remembered %s account %q", agentName(agent), user)
		}
		f.Logins = append(f.Logins[:i], f.Logins[i+1:]...)
		return true, nil
	})
}

// AddLogin runs the agent's own sign-in, then remembers the account it ends up
// signed in as, and returns that account's email. The copy of the previous
// sign-in is taken first, so it is not lost when the agent replaces it.
func AddLogin(agent string) (string, error) {
	if !knownAgent(agent) {
		return "", unknownAgent(agent)
	}
	remember(agent)
	if err := loginCommand(agent); err != nil {
		return "", err
	}
	forgetClaudeCredential()
	forgetClaudeStatus()
	live, present := currentLogin(agent)
	if !present || live.user == "" {
		return "", fmt.Errorf("no %s sign-in with an email was recorded; the login did not finish", agentName(agent))
	}
	remember(agent)
	return live.user, nil
}

// storedClaudeToken returns a usable access token for a remembered Claude
// account that is not the current login, refreshing it from its own copy.
func storedClaudeToken(ctx context.Context, user string) (string, error) {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	l, ok := storedLogin("claude", user)
	if !ok || l.Cred == nil {
		return "", fmt.Errorf("no saved Claude Code sign-in for %s", user)
	}
	b, _ := json.Marshal(l.Cred)
	c, ok := parseClaudeCredentials(b)
	if !ok {
		return "", errors.New("Claude Code is signed out; run claude auth login")
	}
	if claudeFresh(c) {
		return c.OAuth.AccessToken, nil
	}
	if err := claudeRefresh(ctx, &c); err != nil {
		return "", err
	}
	raw, err := c.marshal()
	if err != nil {
		return "", err
	}
	var m map[string]any
	json.Unmarshal(raw, &m)
	if err := editLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f, "claude", user)
		if i < 0 {
			return false, fmt.Errorf("no remembered Claude Code account %q", user)
		}
		f.Logins[i].Cred = m
		return true, nil
	}); err != nil {
		return "", err
	}
	return c.OAuth.AccessToken, nil
}

// storedCodexToken returns a usable token for a remembered ChatGPT account that
// is not the current login, refreshing it from its own copy.
func storedCodexToken(ctx context.Context, user string) (string, string, error) {
	return codexTokenFrom(ctx, loginSource{
		read: func() (map[string]any, bool) {
			l, ok := storedLogin("codex", user)
			if !ok || l.Cred == nil {
				return nil, false
			}
			return l.Cred, true
		},
		write: func(raw map[string]any) error {
			return editLogins(func(f *loginsFile) (bool, error) {
				i := findLogin(f, "codex", user)
				if i < 0 {
					return false, fmt.Errorf("no remembered Codex account %q", user)
				}
				f.Logins[i].Cred = raw
				return true, nil
			})
		},
	})
}

// storedAccount is a remembered account that is not the current login, as a
// signed-in Account whose requests use its own copy of the sign-in.
func storedAccount(l rememberedLogin) *Account {
	user := l.User
	acct := &Account{Agent: l.Agent, User: l.User, Plan: l.Plan}
	switch l.Agent {
	case "codex":
		acct.Stream = true
		acct.sign = codexSigner(func(ctx context.Context) (string, string, error) { return storedCodexToken(ctx, user) })
		acct.body = codexBody
		acct.models = catalog.Codex
		acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
	case "claude":
		acct.sign = claudeSigner(func(ctx context.Context) (string, error) { return storedClaudeToken(ctx, user) })
		acct.body = claudeBody
		acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
	}
	return acct
}

// LoginTargets lists the sign-ins a request to this account may use, in the
// order to try them: the agent's current login first, then the other logins that
// are on, in the order they were remembered. A provider that is not an account
// has none.
func (p Provider) LoginTargets() []Provider {
	if p.Account == nil {
		return nil
	}
	out := []Provider{p}
	for _, l := range loadLogins().Logins {
		if l.Agent != p.Account.Agent || !l.On || l.Cred == nil || sameUser(l.User, p.Account.User) {
			continue
		}
		v := p
		v.Account = storedAccount(l)
		out = append(out, v)
	}
	return out
}
