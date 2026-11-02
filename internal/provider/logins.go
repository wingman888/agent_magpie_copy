package provider

// Several subscription accounts per agent.
//
// Claude Code and Codex hold one login at a time. magpie remembers every
// account the agent has been signed in to, in accounts.json next to
// providers.json (mode 0600, this machine only), so the user can switch
// between them without signing in again, and let more than one work at once.
//
// Who holds a refresh token: the agent holds the one of the account it is
// signed in to (magpie keeps a mirror of it, refreshed whenever it looks, and
// never refreshes it itself); magpie holds, and refreshes, the tokens of every
// other account. A switch hands the tokens over: the account leaving takes
// the agent's latest copy with it into magpie's store, and the account
// arriving is written into the agent's own files with everything else those
// files contain left as it was. A "switched on" account is used by the
// gateway with its own tokens, which are never written to the agent's store.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Login is a subscription account magpie remembers.
type Login struct {
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Plan    string `json:"plan,omitempty"`
	Current bool   `json:"current"`         // the agent is signed in to it right now
	On      bool   `json:"on"`              // used by the gateway too; always true for the current one
	Lost    bool   `json:"lost,omitempty"` // magpie has no credentials for it (sign in again)
}

// savedLogin is what accounts.json keeps per account.
type savedLogin struct {
	Agent     string         `json:"agent"`
	User      string         `json:"user"`
	Plan      string         `json:"plan,omitempty"`
	On        bool           `json:"on,omitempty"`
	Auth      map[string]any `json:"auth,omitempty"`      // claude: claudeAiOauth; codex: tokens
	Profile   map[string]any `json:"profile,omitempty"`   // claude: oauthAccount of ~/.claude.json
	Refreshed string         `json:"refreshed,omitempty"` // codex: last_refresh
}

type loginFile struct {
	Logins []savedLogin `json:"logins"`
}

var (
	loginMu   sync.Mutex // guards accounts.json
	refreshMu sync.Mutex // one switch or refresh of a saved account at a time; taken before loginMu

	syncMu sync.Mutex
	syncFP = map[string]string{} // agent → what the last look at the agent's login found
)

// LoginAgent resolves an agent name that has switchable accounts.
func LoginAgent(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func loginsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

func loadLogins() loginFile {
	var f loginFile
	if b, err := os.ReadFile(loginsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func storeLogins(f loginFile) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(loginsPath(), append(b, '\n'), 0o600, false)
}

// writeFileAtomic replaces a file without ever leaving it half written. With
// keepMode an existing file keeps its permissions, and a symlink is followed.
func writeFileAtomic(path string, b []byte, perm os.FileMode, keepMode bool) error {
	if keepMode {
		if r, err := filepath.EvalSymlinks(path); err == nil {
			path = r
		}
		if st, err := os.Stat(path); err == nil {
			perm = st.Mode().Perm()
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".magpie-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// readRawJSON reads a JSON object whole, numbers kept exactly as written.
func readRawJSON(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func writeRawJSON(path string, m map[string]any) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'), 0o600, true)
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	b, _ := json.Marshal(m)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out map[string]any
	dec.Decode(&out)
	return out
}

func sameJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func indexLogin(f loginFile, agent, user string) int {
	for i, l := range f.Logins {
		if l.Agent == agent && strings.EqualFold(l.User, strings.TrimSpace(user)) {
			return i
		}
	}
	return -1
}

// ---- what the agents hold ---------------------------------------------------------

// claudeConfigFile is where Claude Code keeps the profile of its account.
func claudeConfigFile() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

type claudeProfile struct {
	email string
	oauth map[string]any
}

var claudeProfileCache struct {
	sync.Mutex
	path string
	mod  time.Time
	size int64
	val  claudeProfile
}

// claudeFileProfile reads the account block of ~/.claude.json. The file is big
// and read on every request, so it is parsed again only when it changed.
func claudeFileProfile() claudeProfile {
	path := claudeConfigFile()
	st, err := os.Stat(path)
	if err != nil {
		return claudeProfile{}
	}
	c := &claudeProfileCache
	c.Lock()
	defer c.Unlock()
	if c.path == path && c.mod.Equal(st.ModTime()) && c.size == st.Size() {
		return c.val
	}
	var v claudeProfile
	if raw, err := readRawJSON(path); err == nil {
		if o, ok := raw["oauthAccount"].(map[string]any); ok {
			v.oauth = o
			v.email, _ = o["emailAddress"].(string)
			v.email = strings.TrimSpace(v.email)
		}
	}
	c.path, c.mod, c.size, c.val = path, st.ModTime(), st.Size(), v
	return v
}

// claudeLogin describes the account Claude Code is signed in to.
func claudeLogin(c claudeCredentials, user, plan string) savedLogin {
	auth := map[string]any{}
	if o, ok := c.raw["claudeAiOauth"].(map[string]any); ok {
		for k, v := range o {
			auth[k] = v
		}
	}
	auth["accessToken"] = c.OAuth.AccessToken
	auth["refreshToken"] = c.OAuth.RefreshToken
	auth["expiresAt"] = c.OAuth.ExpiresAt
	return savedLogin{Agent: "claude", User: user, Plan: plan, Auth: cloneMap(auth), Profile: cloneMap(claudeFileProfile().oauth)}
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// codexLogin describes the account Codex is signed in to.
func codexLogin(path, user, plan string) savedLogin {
	l := savedLogin{Agent: "codex", User: user, Plan: plan}
	if raw, err := readRawJSON(path); err == nil {
		l.Auth = cloneMap(func() map[string]any { m, _ := raw["tokens"].(map[string]any); return m }())
		l.Refreshed, _ = raw["last_refresh"].(string)
	}
	return l
}

// agentLogin reads the account the agent is signed in to right now. An agent
// that does not say who it is has no login magpie can remember.
func agentLogin(agent string, fresh bool) (savedLogin, bool) {
	switch agent {
	case "claude":
		var c claudeCredentials
		var ok bool
		if fresh {
			forgetClaudeStatus()
			c, _, ok = readClaudeCredential()
		} else {
			c, _, ok = claudeCredential()
		}
		if !ok {
			return savedLogin{}, false
		}
		user, statusPlan, out := claudeIdentity()
		if out {
			return savedLogin{}, false
		}
		if user == "" {
			user = claudeFileProfile().email
		}
		if user == "" {
			return savedLogin{}, false
		}
		plan := c.OAuth.SubscriptionType
		if statusPlan != "" {
			plan = statusPlan
		}
		return claudeLogin(c, user, plan), true
	case "codex":
		path := codexAuthPath()
		var a codexAuth
		if !readJSON(path, &a) || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return savedLogin{}, false
		}
		id := jwtClaims(a.Tokens.IDToken)
		user := claimString(id, "email")
		if user == "" {
			return savedLogin{}, false
		}
		return codexLogin(path, user, claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")), true
	}
	return savedLogin{}, false
}

// rememberLogin records the account an agent is signed in to, or refreshes
// the copy magpie has of it. An account seen this way starts switched off.
func rememberLogin(cur savedLogin, force bool) {
	b, _ := json.Marshal(cur)
	fp := loginsPath() + "\x00" + string(b)
	syncMu.Lock()
	same := syncFP[cur.Agent] == fp
	syncMu.Unlock()
	if same && !force {
		return
	}
	loginMu.Lock()
	err := mergeLogin(cur)
	loginMu.Unlock()
	if err == nil {
		syncMu.Lock()
		syncFP[cur.Agent] = fp
		syncMu.Unlock()
	}
}

func forgetSync() {
	syncMu.Lock()
	syncFP = map[string]string{}
	syncMu.Unlock()
}

// mergeLogin stores an account seen in an agent: new accounts are added
// (off), known ones get the agent's latest credentials. Hold loginMu.
func mergeLogin(cur savedLogin) error {
	f := loadLogins()
	i := indexLogin(f, cur.Agent, cur.User)
	if i < 0 {
		cur.On = false
		f.Logins = append(f.Logins, cur)
		return storeLogins(f)
	}
	old := f.Logins[i]
	if old.Plan == cur.Plan && sameJSON(old.Auth, cur.Auth) && old.Refreshed == cur.Refreshed &&
		(cur.Profile == nil || sameJSON(old.Profile, cur.Profile)) {
		return nil
	}
	if cur.Plan != "" {
		f.Logins[i].Plan = cur.Plan
	}
	f.Logins[i].Auth, f.Logins[i].Refreshed = cur.Auth, cur.Refreshed
	if cur.Profile != nil {
		f.Logins[i].Profile = cur.Profile
	}
	return storeLogins(f)
}

func validLoginAgent(agent string) (string, error) {
	a, ok := LoginAgent(agent)
	if !ok {
		return "", fmt.Errorf("unknown agent %q (claude (cc) or codex)", agent)
	}
	return a, nil
}

// ---- listing and editing ------------------------------------------------------------

// Logins lists the accounts magpie remembers for an agent, in the order they
// were first seen.
func Logins(agent string) ([]Login, error) {
	agent, err := validLoginAgent(agent)
	if err != nil {
		return nil, err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	return listLogins(agent, lookFresh), nil
}

type lookMode int

const (
	lookNone   lookMode = iota // only what magpie has stored
	lookCached                 // and the agent's login as the gateway last saw it
	lookFresh                  // and the agent's login, read again now
)

// listLogins notes the agent's current login and lists the agent's accounts.
// Hold loginMu.
func listLogins(agent string, look lookMode) []Login {
	var cur savedLogin
	var have bool
	if look != lookNone {
		if cur, have = agentLogin(agent, look == lookFresh); have {
			mergeLogin(cur)
		}
	}
	f := loadLogins()
	out := []Login{}
	for _, l := range f.Logins {
		if l.Agent != agent {
			continue
		}
		isCur := have && strings.EqualFold(l.User, cur.User)
		out = append(out, Login{Agent: agent, User: l.User, Plan: l.Plan, Current: isCur,
			On: isCur || l.On, Lost: !isCur && len(l.Auth) == 0})
	}
	return out
}

func agentTitle(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// SwitchLogin signs the agent in to a remembered account. The account it was
// signed in to is kept, with its latest credentials, and takes over the
// switched-on state the new one had. Sessions already running keep the old
// account until they are restarted.
func SwitchLogin(agent, user string) (Login, error) {
	agent, err := validLoginAgent(agent)
	if err != nil {
		return Login{}, err
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	ti := indexLogin(f, agent, user)
	if ti < 0 {
		return Login{}, fmt.Errorf("magpie remembers no %s account %q (magpie accounts %s lists them)", agentTitle(agent), user, agent)
	}
	cur, have := agentLogin(agent, true)
	ci := -1
	if have {
		if strings.EqualFold(cur.User, f.Logins[ti].User) {
			// already there: just keep the copy current
			mergeLogin(cur)
			return Login{Agent: agent, User: f.Logins[ti].User, Plan: f.Logins[ti].Plan, Current: true, On: true}, nil
		}
		// the account leaving keeps what the agent holds of it now
		if err := mergeLogin(cur); err != nil {
			return Login{}, err
		}
		f = loadLogins()
		ti = indexLogin(f, agent, user)
		ci = indexLogin(f, agent, cur.User)
	}
	target := f.Logins[ti]
	if len(target.Auth) == 0 {
		return Login{}, fmt.Errorf("magpie has no credentials left for %s; sign in to it again (magpie accounts add %s)", target.User, agent)
	}
	if err := applyLogin(agent, target); err != nil {
		return Login{}, err
	}
	if ci >= 0 {
		f.Logins[ci].On = target.On
	}
	forgetSync()
	if err := storeLogins(f); err != nil {
		return Login{}, err
	}
	return Login{Agent: agent, User: target.User, Plan: target.Plan, Current: true, On: true}, nil
}

// applyLogin writes an account into the agent's own files, leaving every
// other thing in them as it was.
func applyLogin(agent string, l savedLogin) error {
	switch agent {
	case "claude":
		c, loc, ok := readClaudeCredential()
		raw := map[string]any{}
		if ok && c.raw != nil {
			raw = c.raw
		} else {
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
		raw["claudeAiOauth"] = cloneMap(l.Auth)
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		nc, ok := parseClaudeCredentials(b)
		if !ok {
			return errors.New("the saved Claude Code credentials are unusable; sign in to the account again")
		}
		if !loc.keychain {
			if err := os.MkdirAll(filepath.Dir(loc.path), 0o700); err != nil {
				return err
			}
		}
		if err := saveClaudeCredential(loc, nc); err != nil {
			return err
		}
		// the profile Claude Code shows and reports lives in ~/.claude.json
		path := claudeConfigFile()
		cfg, err := readRawJSON(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			cfg = map[string]any{}
			if l.Profile == nil {
				forgetClaudeCredential()
				forgetClaudeStatus()
				return nil
			}
		}
		if l.Profile != nil {
			cfg["oauthAccount"] = cloneMap(l.Profile)
		} else {
			delete(cfg, "oauthAccount")
		}
		if err := writeRawJSON(path, cfg); err != nil {
			return err
		}
		forgetClaudeCredential()
		forgetClaudeStatus()
		return nil
	case "codex":
		path := codexAuthPath()
		raw, err := readRawJSON(path)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			raw = map[string]any{}
		}
		raw["tokens"] = cloneMap(l.Auth)
		raw["auth_mode"] = "chatgpt"
		if l.Refreshed != "" {
			raw["last_refresh"] = l.Refreshed
		}
		return writeRawJSON(path, raw)
	}
	return fmt.Errorf("unknown agent %q", agent)
}

// ForgetLogin makes magpie forget an account. The one the agent is signed in
// to cannot be forgotten.
func ForgetLogin(agent, user string) error {
	agent, err := validLoginAgent(agent)
	if err != nil {
		return err
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	loginMu.Lock()
	defer loginMu.Unlock()
	cur, have := agentLogin(agent, true)
	if have {
		mergeLogin(cur)
	}
	f := loadLogins()
	i := indexLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("magpie remembers no %s account %q", agentTitle(agent), user)
	}
	if have && strings.EqualFold(cur.User, f.Logins[i].User) {
		return fmt.Errorf("%s is the account %s is signed in to; switch to another one first", f.Logins[i].User, agentTitle(agent))
	}
	f.Logins = append(f.Logins[:i:i], f.Logins[i+1:]...)
	forgetSync()
	return storeLogins(f)
}

// SetLoginOn lets a remembered account work at the same time as the agent's
// own login (the gateway hands over to it when the first one runs out), or
// stops that. The account the agent is signed in to always works.
func SetLoginOn(agent, user string, on bool) error {
	agent, err := validLoginAgent(agent)
	if err != nil {
		return err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	cur, have := agentLogin(agent, true)
	if have {
		mergeLogin(cur)
	}
	f := loadLogins()
	i := indexLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("magpie remembers no %s account %q", agentTitle(agent), user)
	}
	if !on && have && strings.EqualFold(cur.User, f.Logins[i].User) {
		return fmt.Errorf("%s is the account %s is signed in to; it always works", f.Logins[i].User, agentTitle(agent))
	}
	if f.Logins[i].On == on {
		return nil
	}
	f.Logins[i].On = on
	return storeLogins(f)
}

// loginRunner signs an agent in through its own login flow, which opens the
// vendor's page in the browser; a var so tests can stand in for it.
var loginRunner = func(ctx context.Context, agent string) error {
	var cmd *exec.Cmd
	switch agent {
	case "claude":
		exe := claudeExecutable()
		if exe == "" {
			return errors.New("the claude command is not installed")
		}
		cmd = exec.CommandContext(ctx, exe, "auth", "login")
	case "codex":
		exe, err := exec.LookPath("codex")
		if err != nil {
			return errors.New("the codex command is not installed")
		}
		cmd = exec.CommandContext(ctx, exe, "login")
	default:
		return fmt.Errorf("unknown agent %q", agent)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// AddLogin lets the user sign in to another account through the agent's own
// login, and remembers it. If the agent was signed in to something else, it is
// put back on that account, and the new one (not the first, so switched on by
// default) works beside it.
func AddLogin(ctx context.Context, agent string) (Login, error) {
	agent, err := validLoginAgent(agent)
	if err != nil {
		return Login{}, err
	}
	refreshMu.Lock()
	loginMu.Lock()
	prev, hadPrev := agentLogin(agent, true)
	if hadPrev {
		mergeLogin(prev)
	}
	loginMu.Unlock()
	refreshMu.Unlock()
	// the sign-in takes as long as the user does; nothing is held meanwhile
	if err := loginRunner(ctx, agent); err != nil {
		return Login{}, fmt.Errorf("%s sign-in: %v", agentTitle(agent), err)
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	loginMu.Lock()
	defer loginMu.Unlock()
	forgetSync()
	cur, ok := agentLogin(agent, true)
	if !ok {
		return Login{}, fmt.Errorf("%s did not report a signed-in account; nothing was added", agentTitle(agent))
	}
	f := loadLogins()
	others := 0
	for _, l := range f.Logins {
		if l.Agent == agent && !strings.EqualFold(l.User, cur.User) {
			others++
		}
	}
	isNew := indexLogin(f, agent, cur.User) < 0
	if err := mergeLogin(cur); err != nil {
		return Login{}, err
	}
	f = loadLogins()
	i := indexLogin(f, agent, cur.User)
	if isNew && others > 0 {
		f.Logins[i].On = true
		if err := storeLogins(f); err != nil {
			return Login{}, err
		}
	}
	l := f.Logins[i]
	same := hadPrev && strings.EqualFold(prev.User, cur.User)
	if hadPrev && !same {
		pi := indexLogin(f, agent, prev.User)
		if pi >= 0 && len(f.Logins[pi].Auth) > 0 {
			if err := applyLogin(agent, f.Logins[pi]); err != nil {
				return Login{}, err
			}
			forgetSync()
			return Login{Agent: agent, User: l.User, Plan: l.Plan, On: l.On}, nil
		}
	}
	return Login{Agent: agent, User: l.User, Plan: l.Plan, Current: true, On: true}, nil
}

// ---- using saved accounts through the gateway -------------------------------------

// OtherLogins lists, for an account provider, the remembered accounts of the
// same agent that are switched on besides the one it stands for, in the order
// they were saved. Each comes as a provider that signs with its own tokens.
func (p Provider) OtherLogins() []Provider {
	if p.Account == nil {
		return nil
	}
	agent, ok := LoginAgent(p.Account.Agent)
	if !ok {
		return nil
	}
	loginMu.Lock()
	f := loadLogins()
	loginMu.Unlock()
	var out []Provider
	for _, l := range f.Logins {
		if l.Agent != agent || !l.On || len(l.Auth) == 0 || strings.EqualFold(l.User, p.Account.User) {
			continue
		}
		q := p
		q.Account = storedAccount(p.Account, l)
		out = append(out, q)
	}
	return out
}

func storedAccount(base *Account, l savedLogin) *Account {
	a := *base
	a.User, a.Plan, a.Stored = l.User, l.Plan, true
	agent, user := l.Agent, l.User
	switch agent {
	case "codex":
		a.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
			tok, id, err := loginToken(ctx, agent, user)
			if err != nil {
				return err
			}
			codexSignHeaders(req, tok, id)
			return nil
		}
	case "claude":
		a.sign = func(ctx context.Context, req *http.Request, body []byte) error {
			tok, _, err := loginToken(ctx, agent, user)
			if err != nil {
				return err
			}
			claudeSignHeaders(req, body, tok)
			return nil
		}
		a.token = func(ctx context.Context) (string, error) {
			tok, _, err := loginToken(ctx, agent, user)
			return tok, err
		}
	}
	return &a
}

// OAuthToken is the access token of a remembered account the agent is not
// signed in to; a request for it runs on that token instead of the agent's
// own login. ok is false for the agent's own account.
func (a *Account) OAuthToken(ctx context.Context) (token string, ok bool, err error) {
	if a == nil || a.token == nil {
		return "", false, nil
	}
	token, err = a.token(ctx)
	return token, true, err
}

// loginToken gives a usable access token for one remembered account. The
// agent's own account is left to the agent's refresh; any other is refreshed
// here, and the new tokens stay in magpie's store only.
func loginToken(ctx context.Context, agent, user string) (tok, accountID string, err error) {
	if cur, ok := agentLogin(agent, false); ok && strings.EqualFold(cur.User, user) {
		if agent == "claude" {
			tok, err = claudeToken(ctx)
			return tok, "", err
		}
		return codexToken(ctx, codexAuthPath())
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	loginMu.Lock()
	f := loadLogins()
	loginMu.Unlock()
	i := indexLogin(f, agent, user)
	if i < 0 {
		return "", "", fmt.Errorf("magpie no longer remembers %s", user)
	}
	l := f.Logins[i]
	update := func(edit func(*savedLogin)) error {
		loginMu.Lock()
		defer loginMu.Unlock()
		f := loadLogins()
		i := indexLogin(f, agent, user)
		if i < 0 {
			return nil
		}
		edit(&f.Logins[i])
		return storeLogins(f)
	}
	str := func(k string) string { s, _ := l.Auth[k].(string); return s }
	switch agent {
	case "codex":
		tok = str("access_token")
		if tok == "" {
			return "", "", fmt.Errorf("magpie has no Codex credentials for %s; sign in to it again", user)
		}
		accountID = str("account_id")
		if accountID == "" {
			accountID = claimString(jwtClaims(str("id_token")), "https://api.openai.com/auth", "chatgpt_account_id")
		}
		if exp, _ := jwtClaims(tok)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
			return tok, accountID, nil
		}
		fresh, err := codexRefresh(ctx, str("refresh_token"))
		if err != nil {
			return "", "", err
		}
		err = update(func(l *savedLogin) {
			l.Auth["access_token"] = fresh.AccessToken
			if fresh.IDToken != "" {
				l.Auth["id_token"] = fresh.IDToken
			}
			if fresh.RefreshToken != "" {
				l.Auth["refresh_token"] = fresh.RefreshToken
			}
			l.Refreshed = time.Now().UTC().Format(time.RFC3339Nano)
		})
		return fresh.AccessToken, accountID, err
	case "claude":
		b, _ := json.Marshal(l.Auth)
		var a claudeAuth
		json.Unmarshal(b, &a)
		if claudeFresh(claudeCredentials{OAuth: a}) {
			return a.AccessToken, "", nil
		}
		if a.RefreshToken == "" {
			return "", "", fmt.Errorf("the Claude Code sign-in of %s expired; sign in to it again", user)
		}
		fresh, err := claudeRefresh(ctx, a.RefreshToken)
		if err != nil {
			return "", "", err
		}
		err = update(func(l *savedLogin) {
			l.Auth["accessToken"] = fresh.AccessToken
			if fresh.RefreshToken != "" {
				l.Auth["refreshToken"] = fresh.RefreshToken
			}
			if fresh.ExpiresIn > 0 {
				l.Auth["expiresAt"] = time.Now().Add(time.Duration(fresh.ExpiresIn) * time.Second).UnixMilli()
			}
		})
		return fresh.AccessToken, "", err
	}
	return "", "", fmt.Errorf("unknown agent %q", agent)
}

// ---- allowance of every account ----------------------------------------------------

var loginUsageCache struct {
	sync.Mutex
	path string
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota // agent → email → quota
}

// LoginUsage reports how much of its 5-hour and weekly allowance each
// remembered account of an agent has used, by email. Answers are reused for a
// minute; an account that cannot be asked keeps its last known figures.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	agent, ok := LoginAgent(agent)
	if !ok {
		return out
	}
	c := &loginUsageCache
	c.Lock()
	defer c.Unlock()
	path := loginsPath()
	if c.path != path || c.data == nil {
		c.path, c.at, c.data = path, map[string]time.Time{}, map[string]map[string]SubscriptionQuota{}
	}
	known := c.data[agent]
	if known == nil {
		known = map[string]SubscriptionQuota{}
	}
	loginMu.Lock()
	list := listLogins(agent, lookCached)
	loginMu.Unlock()
	if time.Since(c.at[agent]) < time.Minute {
		complete := true
		for _, l := range list {
			if _, ok := known[l.User]; !ok {
				complete = false
			}
		}
		if complete {
			for _, l := range list {
				out[l.User] = known[l.User]
			}
			return out
		}
	}
	loginMu.Lock()
	list = listLogins(agent, lookFresh)
	loginMu.Unlock()
	type result struct {
		user string
		q    SubscriptionQuota
	}
	results := make([]result, len(list))
	var wg sync.WaitGroup
	fctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	for i, l := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = result{l.User, loginQuota(fctx, l)}
		}()
	}
	wg.Wait()
	next := map[string]SubscriptionQuota{}
	for _, r := range results {
		q := r.q
		if prev, ok := known[r.user]; ok && q.Error != "" && prev.Error == "" {
			q = prev
		}
		next[r.user] = q
		out[r.user] = q
	}
	if ctx.Err() == nil {
		c.data[agent], c.at[agent] = next, time.Now()
	}
	return out
}

func loginQuota(ctx context.Context, l Login) SubscriptionQuota {
	q := SubscriptionQuota{Windows: []QuotaWindow{}, Plan: l.Plan}
	tok, accountID, err := loginToken(ctx, l.Agent, l.User)
	switch l.Agent {
	case "claude":
		q.Provider, q.Name, q.Icon = "claude", "Claude Code", "claude-color"
		if err != nil {
			q.Error = err.Error()
			return q
		}
		return claudeUsageWith(ctx, q, tok, l.Plan)
	default:
		q.Provider, q.Name, q.Icon = "codex", "Codex", "codex-color"
		if err != nil {
			q.Error = err.Error()
			return q
		}
		return codexUsageWith(ctx, q, tok, accountID)
	}
}
