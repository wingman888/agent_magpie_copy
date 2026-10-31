package provider

// Logins: the subscription accounts (Claude Code, Codex) the user has signed
// in to, remembered so they can be switched between and used side by side.
//
// An agent keeps one login at a time. magpie notes every login it sees in
// logins.json (next to providers.json, mode 0600) together with the
// credentials, so a login the agent has since replaced is not lost.
//
// Who holds a refresh token: the agent's own store holds the current
// account's. Every other account's lives only in logins.json, and only magpie
// refreshes it, writing the rotated pair back there. An account that is
// active alongside the current one never touches the agent's files.
//
// The copy kept of the current account is a backup, refreshed whenever
// magpie looks at the agent; a switch reads the agent's store first, so the
// account being replaced keeps its latest credentials.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

// savedLogin is one remembered account.
type savedLogin struct {
	User string `json:"user"`
	Plan string `json:"plan,omitempty"`
	On   bool   `json:"on,omitempty"` // also used by the gateway while another account is current

	// Cred is what the agent keeps as its token set: Claude's claudeAiOauth
	// object, Codex's tokens object.
	Cred map[string]any `json:"cred,omitempty"`
	// Profile is the rest of the identity the agent keeps elsewhere: Claude's
	// oauthAccount (in .claude.json), Codex's auth_mode and last_refresh.
	Profile map[string]any `json:"profile,omitempty"`
}

type loginFile map[string][]savedLogin

var loginMu sync.Mutex

// LoginsPath is where the remembered accounts live.
func LoginsPath() string { return filepath.Join(filepath.Dir(Path()), "logins.json") }

func decodeMap(b []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if m == nil {
		return nil, errors.New("not an object")
	}
	return m, nil
}

// normMap deep-copies a JSON object through its encoding, numbers kept exact.
func normMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	m, err := decodeMap(b)
	if err != nil || len(m) == 0 {
		return nil
	}
	return m
}

func loadLogins() loginFile {
	f := loginFile{}
	if b, err := os.ReadFile(LoginsPath()); err == nil {
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		dec.Decode(&f)
	}
	if f == nil {
		f = loginFile{}
	}
	return f
}

func storeLogins(f loginFile) error {
	p := LoginsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".logins-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// LoginAgents are the agents whose accounts can be remembered.
var LoginAgents = []string{"claude", "codex"}

// ErrLoginAgent is returned for an agent that has no switchable accounts.
type ErrLoginAgent string

func (e ErrLoginAgent) Error() string {
	return fmt.Sprintf("unknown agent %q — accounts work for claude (alias cc) and codex", string(e))
}

// LoginAgent resolves an agent name ("cc" is Claude Code).
func LoginAgent(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", ErrLoginAgent(name)
}

// liveLogin is the account an agent is signed in to right now.
type liveLogin struct {
	user, plan string
	cred       map[string]any
	profile    map[string]any
}

func currentLogin(agent string, fresh bool) (liveLogin, bool) {
	switch agent {
	case "claude":
		return currentClaudeLogin(fresh)
	case "codex":
		return currentCodexLogin()
	}
	return liveLogin{}, false
}

func claudeConfigFile() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// claudeProfile is the oauthAccount Claude Code keeps in .claude.json.
func claudeProfile() map[string]any {
	b, err := os.ReadFile(claudeConfigFile())
	if err != nil {
		return nil
	}
	m, err := decodeMap(b)
	if err != nil {
		return nil
	}
	o, _ := m["oauthAccount"].(map[string]any)
	return normMap(o)
}

func claudeProfileEmail() string {
	s, _ := claudeProfile()["emailAddress"].(string)
	return strings.TrimSpace(s)
}

func currentClaudeLogin(fresh bool) (liveLogin, bool) {
	var c claudeCredentials
	var ok bool
	if fresh {
		c, _, ok = readClaudeCredential()
	} else {
		c, _, ok = claudeCredential()
	}
	if !ok {
		return liveLogin{}, false
	}
	user, statusPlan, signedOut := claudeIdentity()
	if signedOut {
		return liveLogin{}, false
	}
	profile := claudeProfile()
	if user == "" {
		user, _ = profile["emailAddress"].(string)
		user = strings.TrimSpace(user)
	}
	if user == "" {
		return liveLogin{}, false // an account magpie cannot name is not remembered
	}
	if e, _ := profile["emailAddress"].(string); !strings.EqualFold(strings.TrimSpace(e), user) {
		profile = nil // belongs to another account
	}
	plan := c.OAuth.SubscriptionType
	if statusPlan != "" {
		plan = statusPlan
	}
	return liveLogin{user: user, plan: plan, cred: normMap(c.raw["claudeAiOauth"]), profile: profile}, true
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

func currentCodexLogin() (liveLogin, bool) {
	b, err := os.ReadFile(codexAuthPath())
	if err != nil {
		return liveLogin{}, false
	}
	raw, err := decodeMap(b)
	if err != nil {
		return liveLogin{}, false
	}
	toks, _ := raw["tokens"].(map[string]any)
	if s, _ := toks["access_token"].(string); s == "" {
		return liveLogin{}, false
	}
	if mode, _ := raw["auth_mode"].(string); mode == "apikey" {
		return liveLogin{}, false
	}
	idt, _ := toks["id_token"].(string)
	claims := jwtClaims(idt)
	user := strings.TrimSpace(claimString(claims, "email"))
	if user == "" {
		return liveLogin{}, false
	}
	profile := map[string]any{}
	for _, k := range []string{"auth_mode", "last_refresh"} {
		if v, ok := raw[k]; ok {
			profile[k] = v
		}
	}
	return liveLogin{user: user, plan: claimString(claims, "https://api.openai.com/auth", "chatgpt_plan_type"),
		cred: normMap(toks), profile: normMap(profile)}, true
}

func indexLogin(list []savedLogin, user string) int {
	for i, e := range list {
		if strings.EqualFold(e.User, strings.TrimSpace(user)) {
			return i
		}
	}
	return -1
}

// rememberLogin notes the agent's current account.
func rememberLogin(agent string) {
	loginMu.Lock()
	defer loginMu.Unlock()
	syncLocked(agent, false)
}

// syncLocked records the agent's current login in the store (loginMu held)
// and returns it.
func syncLocked(agent string, fresh bool) (liveLogin, bool) {
	live, ok := currentLogin(agent, fresh)
	if !ok {
		return live, false
	}
	f := loadLogins()
	list := f[agent]
	i := indexLogin(list, live.user)
	if i < 0 {
		list = append(list, savedLogin{User: live.user, Plan: live.plan, Cred: live.cred, Profile: live.profile})
	} else {
		e := list[i]
		n := e
		n.User, n.Plan, n.Cred = live.user, live.plan, live.cred
		if live.profile != nil || agent == "codex" {
			n.Profile = live.profile
		}
		if reflect.DeepEqual(e, n) {
			return live, true
		}
		list[i] = n
	}
	f[agent] = list
	storeLogins(f)
	return live, true
}

// Login is one remembered account as callers see it.
type Login struct {
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Plan    string `json:"plan,omitempty"`
	Current bool   `json:"current"` // the agent is signed in to it
	On      bool   `json:"on"`      // used by the gateway too: always true for the current one
	Saved   bool   `json:"saved"`   // magpie holds credentials, so it can be switched to
}

// Logins lists the remembered accounts of an agent in the order they were
// saved, the current one marked.
func Logins(agent string) ([]Login, error) {
	a, err := LoginAgent(agent)
	if err != nil {
		return nil, err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	return listLocked(a, true), nil
}

func listLocked(agent string, fresh bool) []Login {
	live, ok := syncLocked(agent, fresh)
	var out []Login
	for _, e := range loadLogins()[agent] {
		cur := ok && strings.EqualFold(e.User, live.user)
		out = append(out, Login{Agent: agent, User: e.User, Plan: e.Plan, Current: cur, On: cur || e.On, Saved: len(e.Cred) > 0})
	}
	return out
}

// SetLoginOn makes a remembered account active alongside the current one
// (on) or stops that. The account the agent is signed in to is always
// active and cannot be turned off.
func SetLoginOn(agent, user string, on bool) error {
	a, err := LoginAgent(agent)
	if err != nil {
		return err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	live, ok := syncLocked(a, true)
	f := loadLogins()
	i := indexLogin(f[a], user)
	if i < 0 {
		return fmt.Errorf("%s has no remembered account %q — magpie accounts %s lists them", a, user, a)
	}
	if ok && strings.EqualFold(f[a][i].User, live.user) {
		if !on {
			return fmt.Errorf("%s is the account %s is signed in to; it is always active", f[a][i].User, a)
		}
		return nil
	}
	if f[a][i].On == on {
		return nil
	}
	f[a][i].On = on
	return storeLogins(f)
}

// ForgetLogin drops a remembered account. The one the agent is signed in to
// stays.
func ForgetLogin(agent, user string) error {
	a, err := LoginAgent(agent)
	if err != nil {
		return err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	live, ok := syncLocked(a, true)
	f := loadLogins()
	i := indexLogin(f[a], user)
	if i < 0 {
		return fmt.Errorf("%s has no remembered account %q — magpie accounts %s lists them", a, user, a)
	}
	if ok && strings.EqualFold(f[a][i].User, live.user) {
		return fmt.Errorf("%s is the account %s is signed in to now; switch to another account first", f[a][i].User, a)
	}
	f[a] = append(f[a][:i:i], f[a][i+1:]...)
	return storeLogins(f)
}

// SwitchLogin signs the agent in to a remembered account. Sessions already
// running keep the old account until they restart.
func SwitchLogin(agent, user string) (Login, error) {
	a, err := LoginAgent(agent)
	if err != nil {
		return Login{}, err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	return switchLocked(a, user)
}

func switchLocked(a, user string) (Login, error) {
	if a == "claude" {
		forgetClaudeStatus()
	}
	live, ok := syncLocked(a, true) // the outgoing account's latest credentials are saved here
	f := loadLogins()
	list := f[a]
	ti := indexLogin(list, user)
	if ti < 0 {
		return Login{}, fmt.Errorf("%s has no remembered account %q — magpie accounts %s lists them", a, user, a)
	}
	target := list[ti]
	if ok && strings.EqualFold(target.User, live.user) {
		return Login{Agent: a, User: target.User, Plan: target.Plan, Current: true, On: true, Saved: len(target.Cred) > 0}, nil
	}
	if len(target.Cred) == 0 {
		return Login{}, fmt.Errorf("magpie holds no credentials for %s; sign in again with magpie accounts add %s", target.User, a)
	}
	var werr error
	switch a {
	case "claude":
		werr = writeClaudeLogin(target)
		forgetClaudeStatus()
	case "codex":
		werr = writeCodexLogin(target)
	}
	if werr != nil {
		return Login{}, werr
	}
	// the account that steps down takes over the target's active state
	if ok {
		if oi := indexLogin(list, live.user); oi >= 0 {
			list[oi].On, list[ti].On = target.On, list[oi].On
		}
	}
	f[a] = list
	if err := storeLogins(f); err != nil {
		return Login{}, err
	}
	return Login{Agent: a, User: target.User, Plan: target.Plan, Current: true, On: true, Saved: true}, nil
}

// writeClaudeLogin makes Claude Code's stores hold the account: its token
// set in the credentials, its identity in .claude.json. Everything else in
// both files stays as it was.
func writeClaudeLogin(e savedLogin) error {
	cfgPath := claudeConfigFile()
	var cfg map[string]any
	var cfgMode os.FileMode = 0o600
	if b, err := os.ReadFile(cfgPath); err == nil {
		if cfg, err = decodeMap(b); err != nil {
			return fmt.Errorf("%s is not valid JSON; not touching it", cfgPath)
		}
		if st, err := os.Stat(cfgPath); err == nil {
			cfgMode = st.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if cfg == nil {
		cfg = map[string]any{}
	}

	c, loc, ok := readClaudeCredential()
	if !ok {
		dir := os.Getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".claude")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		c, loc = claudeCredentials{raw: map[string]any{}}, claudeCredentialLocation{path: filepath.Join(dir, ".credentials.json")}
		if claudeKeychain {
			loc = claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
		}
	}
	if c.raw == nil {
		c.raw = map[string]any{}
	}
	c.raw["claudeAiOauth"] = normMap(e.Cred)
	b, _ := json.Marshal(c.raw)
	parsed, ok := parseClaudeCredentials(b)
	if !ok {
		return fmt.Errorf("the saved credentials of %s are unusable; sign in again with magpie accounts add claude", e.User)
	}
	if err := saveClaudeCredential(loc, parsed); err != nil {
		return err
	}

	profile := normMap(e.Profile)
	if profile == nil {
		profile = map[string]any{"emailAddress": e.User}
	}
	cfg["oauthAccount"] = profile
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, append(out, '\n'), cfgMode); err != nil {
		return err
	}
	forgetClaudeCredential()
	return nil
}

// writeCodexLogin makes ~/.codex/auth.json hold the account, leaving its
// other fields alone.
func writeCodexLogin(e savedLogin) error {
	path := codexAuthPath()
	raw := map[string]any{}
	var mode os.FileMode = 0o600
	if b, err := os.ReadFile(path); err == nil {
		if raw, err = decodeMap(b); err != nil {
			return fmt.Errorf("%s is not valid JSON; not touching it", path)
		}
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	raw["tokens"] = normMap(e.Cred)
	for k, v := range e.Profile {
		raw[k] = v
	}
	if m, _ := raw["auth_mode"].(string); m == "" || m == "apikey" {
		raw["auth_mode"] = "chatgpt"
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), mode)
}

// ---- adding an account ---------------------------------------------------------

// loginRun signs an agent in through its own CLI, which opens the vendor's
// login page; a var so tests can stand in for the vendor.
var loginRun = func(ctx context.Context, agent string, out io.Writer) error {
	var cmd *exec.Cmd
	switch agent {
	case "claude":
		exe := claudeExecutable()
		if exe == "" {
			return errors.New("Claude Code is not installed (no claude on PATH)")
		}
		cmd = exec.CommandContext(ctx, exe, "auth", "login")
	case "codex":
		exe, err := exec.LookPath("codex")
		if err != nil {
			return errors.New("Codex CLI is not installed (no codex on PATH)")
		}
		cmd = exec.CommandContext(ctx, exe, "login")
	default:
		return ErrLoginAgent(agent)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
	return cmd.Run()
}

// AddLogin has the agent open its vendor's login page, then remembers the
// account that signed in. The account the agent was signed in to stays
// current: the new one is held by magpie, and when it is not the first it is
// switched on to be used alongside.
func AddLogin(ctx context.Context, agent string, out io.Writer) (Login, error) {
	a, err := LoginAgent(agent)
	if err != nil {
		return Login{}, err
	}
	if out == nil {
		out = io.Discard
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	if a == "claude" {
		forgetClaudeStatus()
	}
	before, hadLive := syncLocked(a, true)
	f := loadLogins()
	known := len(f[a])
	knownUsers := map[string]bool{}
	for _, e := range f[a] {
		knownUsers[strings.ToLower(e.User)] = true
	}
	beforeIdx := -1
	beforeOn := false
	if hadLive {
		if beforeIdx = indexLogin(f[a], before.user); beforeIdx >= 0 {
			beforeOn = f[a][beforeIdx].On
		}
	}

	runErr := loginRun(ctx, a, out)
	if a == "claude" {
		forgetClaudeStatus()
	}
	forgetClaudeCredential()
	after, ok := syncLocked(a, true)

	restore := func() {
		if hadLive && ok && !strings.EqualFold(before.user, after.user) {
			if _, err := switchLocked(a, before.user); err == nil {
				g := loadLogins()
				if i := indexLogin(g[a], before.user); i >= 0 {
					g[a][i].On = beforeOn
					storeLogins(g)
				}
			}
		}
	}
	if runErr != nil {
		restore()
		return Login{}, fmt.Errorf("%s login did not finish: %v", a, runErr)
	}
	if !ok {
		return Login{}, fmt.Errorf("no %s login found after signing in", a)
	}
	isNew := !knownUsers[strings.ToLower(after.user)]
	restore()
	g := loadLogins()
	i := indexLogin(g[a], after.user)
	if i < 0 {
		return Login{}, fmt.Errorf("could not remember %s", after.user)
	}
	if isNew && known >= 1 && hadLive && !strings.EqualFold(before.user, after.user) {
		g[a][i].On = true
		if err := storeLogins(g); err != nil {
			return Login{}, err
		}
	}
	cur, _ := currentLogin(a, true)
	e := g[a][i]
	return Login{Agent: a, User: e.User, Plan: e.Plan, Current: strings.EqualFold(cur.user, e.User),
		On: strings.EqualFold(cur.user, e.User) || e.On, Saved: len(e.Cred) > 0}, nil
}

// ---- using accounts side by side -----------------------------------------------

// LoginProviders returns the providers a request may be sent through for an
// account provider: the account the agent is signed in to first, then the
// other remembered accounts switched on, in the order they were saved. A
// provider that is not a subscription account comes back alone.
func LoginProviders(p Provider) []Provider {
	if p.Account == nil || (p.Account.Agent != "claude" && p.Account.Agent != "codex") {
		return []Provider{p}
	}
	agent := p.Account.Agent
	loginMu.Lock()
	list := loadLogins()[agent]
	loginMu.Unlock()
	out := []Provider{p}
	for _, e := range list {
		if !e.On || len(e.Cred) == 0 || strings.EqualFold(e.User, p.Account.User) {
			continue
		}
		out = append(out, loginVariant(p, e))
	}
	return out
}

// loginVariant is p signing in as another remembered account.
func loginVariant(p Provider, e savedLogin) Provider {
	acct := *p.Account
	acct.User, acct.Plan = e.User, e.Plan
	user := e.User
	switch acct.Agent {
	case "claude":
		acct.sign = claudeSigner(func(ctx context.Context) (string, error) {
			if cur, ok := currentLogin("claude", false); ok && strings.EqualFold(cur.user, user) {
				return claudeToken(ctx)
			}
			return savedClaudeToken(ctx, user)
		})
	case "codex":
		acct.sign = codexSigner(func(ctx context.Context) (string, string, error) {
			if cur, ok := currentLogin("codex", false); ok && strings.EqualFold(cur.user, user) {
				return codexToken(ctx, codexAuthPath())
			}
			return savedCodexToken(ctx, user)
		})
	}
	p.Account = &acct
	return p
}

func loginEntry(agent, user string) (savedLogin, bool) {
	loginMu.Lock()
	defer loginMu.Unlock()
	list := loadLogins()[agent]
	if i := indexLogin(list, user); i >= 0 {
		return list[i], true
	}
	return savedLogin{}, false
}

// updateLogin changes a remembered account in the store.
func updateLogin(agent, user string, fn func(e *savedLogin)) {
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	if i := indexLogin(f[agent], user); i >= 0 {
		if f[agent][i].Cred == nil {
			f[agent][i].Cred = map[string]any{}
		}
		fn(&f[agent][i])
		storeLogins(f)
	}
}

// savedClaudeToken is the access token of a remembered Claude account that
// is not the current one, refreshed here and written back to magpie's store
// only: Claude Code's own credentials are not touched.
func savedClaudeToken(ctx context.Context, user string) (string, error) {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	e, ok := loginEntry("claude", user)
	if !ok || len(e.Cred) == 0 {
		return "", fmt.Errorf("magpie holds no credentials for %s; sign in again with magpie accounts add claude", user)
	}
	var au claudeAuth
	b, _ := json.Marshal(e.Cred)
	json.Unmarshal(b, &au)
	if claudeFresh(claudeCredentials{OAuth: au}) {
		return au.AccessToken, nil
	}
	if au.RefreshToken == "" {
		return "", fmt.Errorf("the Claude token of %s expired; sign in again with magpie accounts add claude", user)
	}
	access, refresh, exp, err := claudeRefresh(ctx, au.RefreshToken)
	if err != nil {
		return "", err
	}
	updateLogin("claude", user, func(e *savedLogin) {
		e.Cred["accessToken"] = access
		if refresh != "" {
			e.Cred["refreshToken"] = refresh
		}
		if exp > 0 {
			e.Cred["expiresAt"] = exp
		}
	})
	return access, nil
}

// savedCodexToken is savedClaudeToken for a ChatGPT account.
func savedCodexToken(ctx context.Context, user string) (tok, accountID string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	e, ok := loginEntry("codex", user)
	if !ok || len(e.Cred) == 0 {
		return "", "", fmt.Errorf("magpie holds no credentials for %s; sign in again with magpie accounts add codex", user)
	}
	str := func(k string) string { s, _ := e.Cred[k].(string); return s }
	accountID = str("account_id")
	if accountID == "" {
		accountID = claimString(jwtClaims(str("id_token")), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(str("access_token"))["exp"].(float64); str("access_token") != "" &&
		(exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute) {
		return str("access_token"), accountID, nil
	}
	fresh, err := codexRefresh(ctx, str("refresh_token"))
	if err != nil {
		return "", "", err
	}
	updateLogin("codex", user, func(e *savedLogin) {
		e.Cred["access_token"] = fresh.AccessToken
		if fresh.IDToken != "" {
			e.Cred["id_token"] = fresh.IDToken
		}
		if fresh.RefreshToken != "" {
			e.Cred["refresh_token"] = fresh.RefreshToken
		}
		if e.Profile == nil {
			e.Profile = map[string]any{}
		}
		e.Profile["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	})
	return fresh.AccessToken, accountID, nil
}
