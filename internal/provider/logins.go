package provider

// Remembered subscription accounts: Claude Code and Codex hold one login at
// a time, so magpie keeps the ones the agent has been signed in to in
// logins.json (next to providers.json, mode 0600, never anywhere else) and
// lets the user switch between them, forget them, or have several work at
// once through the gateway.
//
// Who holds a refresh token. The agent's own store holds the signed-in
// account's; a copy of it is kept as a backup so the account survives the
// agent signing in as somebody else. Every other account's tokens are held by
// magpie alone: the gateway refreshes them in logins.json and never writes
// them into the agent's store, so two holders never race to rotate one token
// and sign each other out.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// storedLogin is one remembered account.
type storedLogin struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	// Live: the account serves gateway requests alongside the agent's own
	// login (taking over when that one is out of quota).
	Live bool `json:"live,omitempty"`
	// Cred is what the agent keeps for the account. Codex: {"tokens":…,
	// "last_refresh":…} from auth.json. Claude: the claudeAiOauth object.
	Cred json.RawMessage `json:"cred,omitempty"`
	// Profile is Claude Code's oauthAccount entry in ~/.claude.json.
	Profile json.RawMessage `json:"profile,omitempty"`
}

type loginFile struct {
	Logins []storedLogin `json:"logins"`
}

var loginMu sync.Mutex // guards logins.json

// LoginsPath is where the remembered accounts live.
func LoginsPath() string { return filepath.Join(filepath.Dir(Path()), "logins.json") }

func loadLogins() []storedLogin {
	var f loginFile
	if b, err := os.ReadFile(LoginsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f.Logins
}

func saveLogins(all []storedLogin) error {
	p := LoginsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(loginFile{Logins: all}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

func loginIndex(all []storedLogin, agent, user string) int {
	for i, l := range all {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			return i
		}
	}
	return -1
}

// LoginAgent maps what the user typed to an agent id: claude (alias cc) or
// codex.
func LoginAgent(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func needAgent(agent string) (string, error) {
	a, ok := LoginAgent(agent)
	if !ok {
		return "", fmt.Errorf("unknown agent %q (claude, alias cc, or codex)", agent)
	}
	return a, nil
}

// rememberLogin records the account the agent is signed in to: new ones are
// added (live as given), known ones get their latest tokens.
func rememberLogin(l storedLogin, live bool) {
	loginMu.Lock()
	defer loginMu.Unlock()
	rememberLocked(l, live)
}

func rememberLocked(l storedLogin, live bool) {
	if l.User == "" || len(l.Cred) == 0 {
		return
	}
	all := loadLogins()
	i := loginIndex(all, l.Agent, l.User)
	if i < 0 {
		l.Live = live
		saveLogins(append(all, l))
		return
	}
	cur := all[i]
	if l.Plan == "" {
		l.Plan = cur.Plan
	}
	if len(l.Profile) == 0 {
		l.Profile = cur.Profile
	}
	same := func(a, b json.RawMessage) bool { return string(a) == string(b) }
	if cur.Plan == l.Plan && same(cur.Cred, l.Cred) && same(cur.Profile, l.Profile) {
		return
	}
	cur.Plan, cur.Cred, cur.Profile = l.Plan, l.Cred, l.Profile
	all[i] = cur
	saveLogins(all)
}

// ---- reading and writing the agents' own stores -----------------------------------

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

func readRawFile(path string) map[string]json.RawMessage {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// writeRawFile writes a JSON object back, keeping the file's permissions
// (0600 for a new one).
func writeRawFile(path string, m map[string]json.RawMessage) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), mode)
}

// codexLogin reads the account Codex CLI is signed in to from its auth.json.
func codexLogin(path, user, plan string) storedLogin {
	raw := readRawFile(path)
	cred := map[string]json.RawMessage{}
	for _, k := range []string{"tokens", "last_refresh"} {
		if v, ok := raw[k]; ok {
			cred[k] = v
		}
	}
	b, _ := json.Marshal(cred)
	return storedLogin{Agent: "codex", User: user, Plan: plan, Cred: b}
}

func installCodex(l storedLogin) error {
	var cred map[string]json.RawMessage
	if json.Unmarshal(l.Cred, &cred) != nil || len(cred["tokens"]) == 0 {
		return fmt.Errorf("magpie holds no Codex credentials for %s; sign in to them with magpie accounts add codex", l.User)
	}
	path := codexAuthPath()
	raw := readRawFile(path)
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	raw["auth_mode"] = json.RawMessage(`"chatgpt"`)
	raw["tokens"] = cred["tokens"]
	if v, ok := cred["last_refresh"]; ok {
		raw["last_refresh"] = v
	}
	return writeRawFile(path, raw)
}

// claudeGlobalPath is the file Claude Code keeps its profile in.
func claudeGlobalPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

type claudeProfile struct {
	email string
	raw   json.RawMessage
}

// claudeFileProfile reads who Claude Code says is signed in from its profile.
func claudeFileProfile() claudeProfile {
	raw := readRawFile(claudeGlobalPath())["oauthAccount"]
	var a struct {
		Email string `json:"emailAddress"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &a) != nil {
		return claudeProfile{}
	}
	return claudeProfile{email: strings.TrimSpace(a.Email), raw: raw}
}

// claudeLogin snapshots Claude Code's current credentials and profile.
func claudeLogin(c claudeCredentials, user, plan string) storedLogin {
	var oauth any = c.raw["claudeAiOauth"]
	if m, ok := oauth.(map[string]any); ok {
		// make sure the typed fields (possibly refreshed) are what is kept
		b, _ := c.marshal()
		var full map[string]any
		if json.Unmarshal(b, &full) == nil {
			if o, ok := full["claudeAiOauth"].(map[string]any); ok {
				m = o
			}
		}
		oauth = m
	} else {
		oauth = c.OAuth
	}
	b, _ := json.Marshal(oauth)
	return storedLogin{Agent: "claude", User: user, Plan: plan, Cred: b, Profile: claudeFileProfile().raw}
}

func installClaude(l storedLogin) error {
	var oauth map[string]json.RawMessage
	if json.Unmarshal(l.Cred, &oauth) != nil || len(oauth["accessToken"]) == 0 {
		return fmt.Errorf("magpie holds no Claude Code credentials for %s; sign in to them with magpie accounts add claude", l.User)
	}
	forgetClaudeCredential()
	c, loc, ok := readClaudeCredential()
	merged := map[string]any{}
	if ok {
		for k, v := range c.raw {
			merged[k] = v
		}
	} else {
		dir := os.Getenv("CLAUDE_CONFIG_DIR")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".claude")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		loc = claudeCredentialLocation{path: filepath.Join(dir, ".credentials.json")}
	}
	var oauthAny any
	json.Unmarshal(l.Cred, &oauthAny)
	merged["claudeAiOauth"] = oauthAny
	mb, _ := json.Marshal(merged)
	nc, ok := parseClaudeCredentials(mb)
	if !ok {
		return errors.New("the remembered Claude Code credentials are unreadable")
	}
	if err := saveClaudeCredential(loc, nc); err != nil {
		return err
	}
	if len(l.Profile) > 0 {
		path := claudeGlobalPath()
		raw := readRawFile(path)
		if raw == nil {
			raw = map[string]json.RawMessage{}
		}
		raw["oauthAccount"] = l.Profile
		if err := writeRawFile(path, raw); err != nil {
			return err
		}
	}
	forgetClaudeStatus()
	return nil
}

// currentLogin reads the account the agent is signed in to, from the agent.
func currentLogin(agent string) (storedLogin, bool) {
	switch agent {
	case "codex":
		path := codexAuthPath()
		var a codexAuth
		if !readJSON(path, &a) || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return storedLogin{}, false
		}
		id := jwtClaims(a.Tokens.IDToken)
		user := claimString(id, "email")
		if user == "" {
			return storedLogin{}, false
		}
		return codexLogin(path, user, claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")), true
	case "claude":
		forgetClaudeCredential()
		c, _, ok := claudeCredential()
		if !ok {
			return storedLogin{}, false
		}
		user, plan, out := claudeIdentity()
		if out {
			return storedLogin{}, false
		}
		if user == "" {
			user = claudeFileProfile().email
		}
		if user == "" {
			return storedLogin{}, false
		}
		if plan == "" {
			plan = c.OAuth.SubscriptionType
		}
		return claudeLogin(c, user, plan), true
	}
	return storedLogin{}, false
}

func installLogin(l storedLogin) error {
	switch l.Agent {
	case "codex":
		return installCodex(l)
	case "claude":
		return installClaude(l)
	}
	return fmt.Errorf("unknown agent %q", l.Agent)
}

// syncLogins remembers the account the agent is signed in to now.
func syncLogins(agent string) (cur storedLogin, ok bool) {
	cur, ok = currentLogin(agent)
	if ok {
		rememberLogin(cur, false)
	}
	return cur, ok
}

// ---- the user's verbs ----------------------------------------------------------------

// LoginInfo is one remembered account, for display.
type LoginInfo struct {
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Plan    string `json:"plan,omitempty"`
	Current bool   `json:"current"` // the agent is signed in as this account
	Live    bool   `json:"live"`    // serves gateway requests (always true for the current one)
}

// Logins lists the accounts remembered for an agent, in the order they were
// saved. The account the agent is signed in to now is remembered first.
func Logins(agent string) ([]LoginInfo, error) {
	agent, err := needAgent(agent)
	if err != nil {
		return nil, err
	}
	cur, ok := syncLogins(agent)
	loginMu.Lock()
	defer loginMu.Unlock()
	var out []LoginInfo
	for _, l := range loadLogins() {
		if l.Agent != agent {
			continue
		}
		is := ok && strings.EqualFold(l.User, cur.User)
		out = append(out, LoginInfo{Agent: agent, User: l.User, Plan: l.Plan, Current: is, Live: is || l.Live})
	}
	return out, nil
}

// SwitchLogin signs the agent in to a remembered account. The account it
// replaces keeps its latest tokens, and inherits the "live" state the
// target had, so the set of simultaneous accounts stays as it was.
func SwitchLogin(agent, user string) error {
	agent, err := needAgent(agent)
	if err != nil {
		return err
	}
	cur, haveCur := syncLogins(agent)
	loginMu.Lock()
	defer loginMu.Unlock()
	all := loadLogins()
	ti := loginIndex(all, agent, user)
	if ti < 0 {
		return fmt.Errorf("no remembered %s account %q — magpie accounts %s lists them", agent, user, agent)
	}
	target := all[ti]
	if haveCur && strings.EqualFold(cur.User, target.User) {
		return nil
	}
	if err := installLogin(target); err != nil {
		return err
	}
	if haveCur {
		if oi := loginIndex(all, agent, cur.User); oi >= 0 {
			all[oi].Live = target.Live
		}
	}
	return saveLogins(all)
}

// ForgetLogin drops a remembered account. The one the agent is signed in to
// cannot be forgotten.
func ForgetLogin(agent, user string) error {
	agent, err := needAgent(agent)
	if err != nil {
		return err
	}
	cur, haveCur := syncLogins(agent)
	loginMu.Lock()
	defer loginMu.Unlock()
	all := loadLogins()
	i := loginIndex(all, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q — magpie accounts %s lists them", agent, user, agent)
	}
	if haveCur && strings.EqualFold(cur.User, all[i].User) {
		return fmt.Errorf("%s is the account %s is signed in to; switch to another one first", all[i].User, agent)
	}
	return saveLogins(append(all[:i:i], all[i+1:]...))
}

// SetLoginOn makes a remembered account work alongside the agent's own login
// (or stops it). The signed-in account always works and cannot be turned off.
func SetLoginOn(agent, user string, on bool) error {
	agent, err := needAgent(agent)
	if err != nil {
		return err
	}
	cur, haveCur := syncLogins(agent)
	loginMu.Lock()
	defer loginMu.Unlock()
	all := loadLogins()
	i := loginIndex(all, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q — magpie accounts %s lists them", agent, user, agent)
	}
	if haveCur && strings.EqualFold(cur.User, all[i].User) {
		if !on {
			return fmt.Errorf("%s is the account %s is signed in to; it always works and cannot be turned off", all[i].User, agent)
		}
		return nil
	}
	if on && len(all[i].Cred) == 0 {
		return fmt.Errorf("magpie holds no credentials for %s", all[i].User)
	}
	all[i].Live = on
	return saveLogins(all)
}

// AddLogin lets the user sign in to one more account: login runs the vendor's
// sign-in (which opens its page and returns once it is done). The new account
// is remembered; when it is not the first one it also goes live, and the
// agent is put back on the account it was signed in to.
func AddLogin(agent string, login func() error) ([]LoginInfo, error) {
	agent, err := needAgent(agent)
	if err != nil {
		return nil, err
	}
	prev, havePrev := syncLogins(agent)
	if err := login(); err != nil {
		return nil, err
	}
	forgetClaudeStatus()
	forgetClaudeCredential()
	now, ok := currentLogin(agent)
	if !ok {
		return nil, fmt.Errorf("signed in, but no %s account was found afterwards", agent)
	}
	loginMu.Lock()
	all := loadLogins()
	known := 0
	for _, l := range all {
		if l.Agent == agent {
			known++
		}
	}
	// a second (or later) account works alongside the first from the start
	rememberLocked(now, known > 0)
	if havePrev && !strings.EqualFold(prev.User, now.User) {
		all = loadLogins()
		if i := loginIndex(all, agent, now.User); i >= 0 && !all[i].Live {
			all[i].Live = true
			saveLogins(all)
		}
		if pi := loginIndex(all, agent, prev.User); pi >= 0 {
			if err := installLogin(all[pi]); err != nil {
				loginMu.Unlock()
				return nil, err
			}
		}
	}
	loginMu.Unlock()
	return Logins(agent)
}

// ---- the gateway's view: further accounts of the same agent ---------------------------

// OtherLogins lists the remembered accounts, besides the one the agent is
// signed in to, that are switched on, as providers of their own, in the order
// they were saved. They run on tokens magpie holds.
func (p Provider) OtherLogins() []Provider {
	if p.Account == nil || p.Account.Stored {
		return nil
	}
	agent := p.Account.Agent
	if agent != "claude" && agent != "codex" {
		return nil
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	var out []Provider
	for _, l := range loadLogins() {
		if l.Agent != agent || !l.Live || len(l.Cred) == 0 || strings.EqualFold(l.User, p.Account.User) {
			continue
		}
		out = append(out, storedProvider(p, l))
	}
	return out
}

func storedProvider(base Provider, l storedLogin) Provider {
	a := *base.Account
	a.User, a.Plan, a.Stored = l.User, l.Plan, true
	user := l.User
	switch l.Agent {
	case "codex":
		a.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
			tok, id, err := storedCodexToken(ctx, user)
			if err != nil {
				return err
			}
			codexSignHeaders(req, tok, id)
			return nil
		}
	case "claude":
		a.token = func(ctx context.Context) (string, error) { return storedClaudeToken(ctx, user) }
		a.sign = func(ctx context.Context, req *http.Request, body []byte) error {
			tok, err := storedClaudeToken(ctx, user)
			if err != nil {
				return err
			}
			claudeSignHeaders(req, body, tok)
			return nil
		}
	}
	base.Account = &a
	return base
}

// OAuthToken is the access token of a remembered account that runs on
// tokens magpie holds (own = true); for the agent's own login it is empty,
// and the agent's binary uses its own.
func (a *Account) OAuthToken(ctx context.Context) (tok string, own bool, err error) {
	if a == nil || !a.Stored || a.token == nil {
		return "", false, nil
	}
	tok, err = a.token(ctx)
	return tok, true, err
}

// updateStoredCred rewrites one remembered account's credentials.
func updateStoredCred(agent, user string, edit func(json.RawMessage) json.RawMessage) {
	loginMu.Lock()
	defer loginMu.Unlock()
	all := loadLogins()
	if i := loginIndex(all, agent, user); i >= 0 {
		all[i].Cred = edit(all[i].Cred)
		saveLogins(all)
	}
}

func storedCred(agent, user string) (storedLogin, bool) {
	loginMu.Lock()
	defer loginMu.Unlock()
	all := loadLogins()
	if i := loginIndex(all, agent, user); i >= 0 {
		return all[i], true
	}
	return storedLogin{}, false
}

// storedCodexToken is codexToken for a remembered account: the tokens come
// from, and a refresh goes back to, logins.json — never to Codex's auth.json.
func storedCodexToken(ctx context.Context, user string) (tok, accountID string, err error) {
	codexMu.Lock()
	defer codexMu.Unlock()
	l, ok := storedCred("codex", user)
	var cred struct {
		Tokens struct {
			IDToken      string `json:"id_token"`
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if !ok || json.Unmarshal(l.Cred, &cred) != nil || cred.Tokens.AccessToken == "" {
		return "", "", fmt.Errorf("magpie holds no Codex credentials for %s; run magpie accounts add codex", user)
	}
	t := cred.Tokens
	accountID = t.AccountID
	if accountID == "" {
		accountID = claimString(jwtClaims(t.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(t.AccessToken)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
		return t.AccessToken, accountID, nil
	}
	fresh, err := codexRefresh(ctx, t.RefreshToken)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", user, err)
	}
	updateStoredCred("codex", user, func(old json.RawMessage) json.RawMessage {
		var c map[string]json.RawMessage
		if json.Unmarshal(old, &c) != nil {
			c = map[string]json.RawMessage{}
		}
		var tk map[string]json.RawMessage
		if json.Unmarshal(c["tokens"], &tk) != nil {
			tk = map[string]json.RawMessage{}
		}
		set := func(m map[string]json.RawMessage, k, v string) {
			b, _ := json.Marshal(v)
			m[k] = b
		}
		set(tk, "access_token", fresh.AccessToken)
		if fresh.IDToken != "" {
			set(tk, "id_token", fresh.IDToken)
		}
		if fresh.RefreshToken != "" {
			set(tk, "refresh_token", fresh.RefreshToken)
		}
		c["tokens"], _ = json.Marshal(tk)
		set(c, "last_refresh", time.Now().UTC().Format(time.RFC3339Nano))
		b, _ := json.Marshal(c)
		return b
	})
	return fresh.AccessToken, accountID, nil
}

// storedClaudeToken is claudeToken for a remembered account.
func storedClaudeToken(ctx context.Context, user string) (string, error) {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	l, ok := storedCred("claude", user)
	var auth claudeAuth
	if !ok || json.Unmarshal(l.Cred, &auth) != nil || auth.AccessToken == "" {
		return "", fmt.Errorf("magpie holds no Claude Code credentials for %s; run magpie accounts add claude", user)
	}
	if claudeFresh(claudeCredentials{OAuth: auth}) {
		return auth.AccessToken, nil
	}
	if auth.RefreshToken == "" {
		return "", fmt.Errorf("%s: Claude Code OAuth token expired; run magpie accounts add claude", user)
	}
	fresh, err := claudeRefresh(ctx, auth.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("%s: %w", user, err)
	}
	updateStoredCred("claude", user, func(old json.RawMessage) json.RawMessage {
		var c map[string]json.RawMessage
		if json.Unmarshal(old, &c) != nil {
			c = map[string]json.RawMessage{}
		}
		put := func(k string, v any) {
			b, _ := json.Marshal(v)
			c[k] = b
		}
		put("accessToken", fresh.AccessToken)
		if fresh.RefreshToken != "" {
			put("refreshToken", fresh.RefreshToken)
		}
		if fresh.ExpiresIn > 0 {
			put("expiresAt", time.Now().Add(time.Duration(fresh.ExpiresIn)*time.Second).UnixMilli())
		}
		b, _ := json.Marshal(c)
		return b
	})
	return fresh.AccessToken, nil
}

// ---- usage per account ------------------------------------------------------------------

var loginUsageCache struct {
	sync.Mutex
	at   map[string]time.Time
	data map[string]map[string]SubscriptionQuota
}

// LoginUsage returns the 5 hour / weekly allowance used by each remembered
// account of an agent, by email. A minute of results is reused; an account
// that cannot be asked right now keeps the last answer it gave. Agents without
// remembered accounts (anything but claude and codex) give an empty map.
func LoginUsage(ctx context.Context, agent string) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	agent, ok := LoginAgent(agent)
	if !ok {
		return out
	}
	c := &loginUsageCache
	c.Lock()
	if time.Since(c.at[agent]) < time.Minute && c.data[agent] != nil {
		for k, v := range c.data[agent] {
			out[k] = v
		}
		c.Unlock()
		return out
	}
	prev := map[string]SubscriptionQuota{}
	for k, v := range c.data[agent] {
		prev[k] = v
	}
	c.Unlock()

	logins, _ := Logins(agent)
	type res struct {
		user string
		q    SubscriptionQuota
	}
	ch := make(chan res, len(logins))
	for _, l := range logins {
		go func() { ch <- res{l.User, loginQuota(ctx, l)} }()
	}
	fresh := map[string]SubscriptionQuota{}
	for range logins {
		r := <-ch
		fresh[r.user] = r.q
	}
	merged := map[string]SubscriptionQuota{}
	for _, l := range logins {
		q := fresh[l.User]
		if old, had := prev[l.User]; had && q.Error != "" && old.Error == "" {
			q = old
		}
		merged[l.User] = q
	}
	c.Lock()
	if c.at == nil {
		c.at, c.data = map[string]time.Time{}, map[string]map[string]SubscriptionQuota{}
	}
	c.at[agent], c.data[agent] = time.Now(), merged
	c.Unlock()
	for k, v := range merged {
		out[k] = v
	}
	return out
}

// loginQuota asks the vendor about one account, with that account's own tokens.
func loginQuota(ctx context.Context, l LoginInfo) SubscriptionQuota {
	ctx, cancel := context.WithTimeout(ctx, subscriptionTimeout)
	defer cancel()
	switch l.Agent {
	case "codex":
		q := SubscriptionQuota{Provider: "codex", Name: "Codex", Icon: "codex-color", Plan: l.Plan, Windows: []QuotaWindow{}}
		if l.Current {
			tok, id, err := codexToken(ctx, codexAuthPath())
			if err != nil {
				q.Error = err.Error()
				return q
			}
			return codexUsageWith(ctx, q, tok, id)
		}
		tok, id, err := storedCodexToken(ctx, l.User)
		if err != nil {
			q.Error = err.Error()
			return q
		}
		return codexUsageWith(ctx, q, tok, id)
	case "claude":
		q := SubscriptionQuota{Provider: "claude", Name: "Claude Code", Icon: "claude-color", Plan: l.Plan, Windows: []QuotaWindow{}}
		var tok string
		var err error
		if l.Current {
			tok, err = claudeToken(ctx)
		} else {
			tok, err = storedClaudeToken(ctx, l.User)
		}
		if err != nil {
			q.Error = err.Error()
			return q
		}
		return claudeUsageWith(ctx, q, tok, l.Plan)
	}
	return SubscriptionQuota{Error: "unsupported"}
}
