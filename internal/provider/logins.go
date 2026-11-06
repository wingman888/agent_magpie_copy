package provider

// Logins: the subscription accounts (Claude Code, Codex) magpie remembers.
//
// An agent holds one login at a time. magpie keeps a copy of every login it
// has seen in accounts.json next to providers.json (mode 0600), so the user
// can switch between them. A login marked "on" keeps serving the gateway next
// to the one the agent is signed in as; the agent's own login is always on.
//
// A refresh token has one holder. The agent holds the login it is signed in
// as; a stored login that is not the current one is held by magpie alone,
// and only magpie refreshes it. The copy kept for the current login is
// re-synced from the agent on every start and is never used by the gateway.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// loginAgents are the agents that take several subscription logins.
var loginAgents = []string{"claude", "codex"}

// savedLogin is one remembered subscription login.
type savedLogin struct {
	Agent string          `json:"agent"`           // claude or codex
	User  string          `json:"user"`            // the account's email
	Plan  string          `json:"plan,omitempty"`  // the subscription, when known
	On    bool            `json:"on,omitempty"`    // 同时生效: serves the gateway next to the current login
	Creds json.RawMessage `json:"creds,omitempty"` // the login's own tokens, in the agent's own shape

	current bool // set on the live login only; never stored
}

type loginsFile struct {
	Logins []savedLogin `json:"logins"`
}

// Login is a remembered account, as the CLI lists it.
type Login struct {
	Agent   string
	User    string
	Plan    string
	Current bool // the agent is signed in as this account
	On      bool // serves the gateway next to the current login
}

// loginsMu serializes writes to accounts.json. It is never held across a
// network call, so reading the file (Accounts runs on every request) never waits.
var loginsMu sync.Mutex

// LoginAgent maps an agent name, or its alias, to the id used for logins.
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

func loadLogins() loginsFile {
	var f loginsFile
	if b, err := os.ReadFile(loginsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	for i := range f.Logins {
		f.Logins[i].Creds = compactJSON(f.Logins[i].Creds)
	}
	return f
}

// compactJSON makes a credential blob canonical, so a blob read back from the
// file compares equal to one freshly read from the agent.
func compactJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return nil
	}
	var buf bytes.Buffer
	if json.Compact(&buf, b) != nil {
		return json.RawMessage(b)
	}
	return json.RawMessage(buf.Bytes())
}

func storeLogins(f loginsFile) error {
	p := loginsPath()
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

// updateLogins applies fn to the saved logins under the write lock and stores
// the result when fn reports a change.
func updateLogins(fn func(f *loginsFile) (bool, error)) error {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	f := loadLogins()
	changed, err := fn(&f)
	if err != nil || !changed {
		return err
	}
	return storeLogins(f)
}

func findLogin(ls []savedLogin, agent, user string) int {
	for i, l := range ls {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			return i
		}
	}
	return -1
}

// currentLogin reads what the agent is signed in as right now. ok is false
// when it is signed out, uses an API key, or its account has no email.
func currentLogin(agent string) (savedLogin, bool) {
	switch agent {
	case "claude":
		c, _, ok := claudeCredential()
		if !ok {
			return savedLogin{}, false
		}
		user, plan, signedOut := claudeIdentity()
		if signedOut || user == "" {
			return savedLogin{}, false
		}
		if plan == "" {
			plan = c.OAuth.SubscriptionType
		}
		oauth, ok := c.raw["claudeAiOauth"]
		if !ok {
			return savedLogin{}, false
		}
		creds, err := json.Marshal(oauth)
		if err != nil {
			return savedLogin{}, false
		}
		return savedLogin{Agent: agent, User: user, Plan: plan, Creds: compactJSON(creds), current: true}, true
	case "codex":
		home, _ := os.UserHomeDir()
		b, err := os.ReadFile(codexAuthFile(home))
		if err != nil {
			return savedLogin{}, false
		}
		var raw map[string]any
		var a codexAuth
		if json.Unmarshal(b, &raw) != nil || json.Unmarshal(b, &a) != nil {
			return savedLogin{}, false
		}
		if a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return savedLogin{}, false
		}
		id := jwtClaims(a.Tokens.IDToken)
		user := claimString(id, "email")
		if user == "" {
			return savedLogin{}, false
		}
		creds := map[string]any{"tokens": raw["tokens"]}
		if lr, ok := raw["last_refresh"]; ok {
			creds["last_refresh"] = lr
		}
		blob, _ := json.Marshal(creds)
		return savedLogin{Agent: agent, User: user,
			Plan:  claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type"),
			Creds: compactJSON(blob), current: true}, true
	}
	return savedLogin{}, false
}

func codexAuthFile(home string) string { return filepath.Join(home, ".codex", "auth.json") }

// RememberLogins saves the login each agent is signed in as, and re-syncs the
// copy already saved for it. A login magpie has not seen before is saved; the
// second and later ones start switched on. It writes only when something changed.
func RememberLogins() error {
	forgetClaudeCredential()
	return updateLogins(func(f *loginsFile) (bool, error) {
		changed := false
		for _, agent := range loginAgents {
			cur, ok := currentLogin(agent)
			if !ok {
				continue
			}
			i := findLogin(f.Logins, agent, cur.User)
			if i < 0 {
				cur.On = false
				for _, l := range f.Logins {
					if l.Agent == agent {
						cur.On = true
						break
					}
				}
				f.Logins = append(f.Logins, cur)
				changed = true
				continue
			}
			l := &f.Logins[i]
			if !bytes.Equal(l.Creds, cur.Creds) || l.Plan != cur.Plan {
				l.Creds, l.Plan = cur.Creds, cur.Plan
				changed = true
			}
		}
		return changed, nil
	})
}

// listLogins gives the agent's logins: the one signed in first, then the rest
// in saved order. It does not write.
func listLogins(agent string) []savedLogin {
	cur, has := currentLogin(agent)
	f := loadLogins()
	var out []savedLogin
	if has {
		cur.Creds = nil
		if i := findLogin(f.Logins, agent, cur.User); i >= 0 {
			cur.On = f.Logins[i].On
		}
		out = append(out, cur)
	}
	for _, l := range f.Logins {
		if l.Agent != agent || (has && strings.EqualFold(l.User, cur.User)) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Logins lists the remembered accounts of an agent, the one signed in first.
func Logins(agent string) []Login {
	name, ok := LoginAgent(agent)
	if !ok {
		return nil
	}
	var out []Login
	for _, l := range listLogins(name) {
		out = append(out, Login{Agent: l.Agent, User: l.User, Plan: l.Plan, Current: l.current, On: l.On || l.current})
	}
	return out
}

// SwitchLogin signs the agent in as a remembered account. The account it
// replaces keeps the agent's latest credentials and takes over the on-state
// the new account had. Only the login keys of the agent's own file change.
// The second return is false when that account was already signed in.
func SwitchLogin(agent, user string) (bool, error) {
	name, ok := LoginAgent(agent)
	if !ok {
		return false, fmt.Errorf("unknown agent %q", agent)
	}
	if err := RememberLogins(); err != nil {
		return false, err
	}
	var target savedLogin
	switched := false
	err := updateLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f.Logins, name, user)
		if i < 0 {
			return false, fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", name, user, name)
		}
		target = f.Logins[i]
		cur, has := currentLogin(name)
		if has && strings.EqualFold(cur.User, target.User) {
			return false, nil
		}
		switched = true
		if has {
			if j := findLogin(f.Logins, name, cur.User); j >= 0 {
				f.Logins[j].Creds, f.Logins[j].Plan, f.Logins[j].On = cur.Creds, cur.Plan, target.On
			}
		}
		return true, nil
	})
	if err != nil || !switched {
		return false, err
	}
	if err := writeLogin(name, target.Creds); err != nil {
		return false, err
	}
	forgetClaudeCredential()
	forgetClaudeStatus()
	return true, nil
}

// ForgetLogin removes a remembered account. The account the agent is signed in
// as cannot be forgotten: sign out of the agent first.
func ForgetLogin(agent, user string) error {
	name, ok := LoginAgent(agent)
	if !ok {
		return fmt.Errorf("unknown agent %q", agent)
	}
	if err := RememberLogins(); err != nil {
		return err
	}
	return updateLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f.Logins, name, user)
		if i < 0 {
			return false, fmt.Errorf("no remembered %s account %q", name, user)
		}
		if cur, ok := currentLogin(name); ok && strings.EqualFold(cur.User, user) {
			return false, fmt.Errorf("%s is the account %s is signed in as; sign it out before forgetting it", user, name)
		}
		f.Logins = append(f.Logins[:i], f.Logins[i+1:]...)
		return true, nil
	})
}

// SetLoginOn switches a remembered account on (同时生效) or off. The account
// the agent is signed in as is always on.
func SetLoginOn(agent, user string, on bool) error {
	name, ok := LoginAgent(agent)
	if !ok {
		return fmt.Errorf("unknown agent %q", agent)
	}
	if err := RememberLogins(); err != nil {
		return err
	}
	return updateLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f.Logins, name, user)
		if i < 0 {
			return false, fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", name, user, name)
		}
		if !on {
			if cur, ok := currentLogin(name); ok && strings.EqualFold(cur.User, user) {
				return false, fmt.Errorf("%s is the account %s is signed in as; it is always on", user, name)
			}
		}
		if f.Logins[i].On == on {
			return false, nil
		}
		f.Logins[i].On = on
		return true, nil
	})
}

// otherOnLogins are the switched-on logins other than the current one, in
// saved order. They become gateway candidates after the current login.
func otherOnLogins(agent, current string) []savedLogin {
	var out []savedLogin
	for _, l := range loadLogins().Logins {
		if l.Agent == agent && l.On && !strings.EqualFold(l.User, current) {
			out = append(out, l)
		}
	}
	return out
}

// writeLogin makes the agent sign in with the given credentials.
func writeLogin(agent string, creds json.RawMessage) error {
	switch agent {
	case "claude":
		return writeClaudeLogin(creds)
	case "codex":
		return writeCodexLogin(creds)
	}
	return fmt.Errorf("unknown agent %q", agent)
}

// writeClaudeLogin replaces the claudeAiOauth block of Claude Code's
// credentials, keeping every other key (MCP OAuth state included).
func writeClaudeLogin(creds json.RawMessage) error {
	var oauth map[string]any
	if err := json.Unmarshal(creds, &oauth); err != nil || oauth == nil {
		return errors.New("the saved Claude login is unreadable")
	}
	c, loc, ok := readClaudeCredential()
	if !ok {
		c = claudeCredentials{raw: map[string]any{}}
		loc = claudeCredentialLocation{path: claudeCredentialsPath()}
		if err := os.MkdirAll(filepath.Dir(loc.path), 0o755); err != nil {
			return err
		}
	}
	if c.raw == nil {
		c.raw = map[string]any{}
	}
	c.raw["claudeAiOauth"] = oauth
	json.Unmarshal(creds, &c.OAuth)
	return saveClaudeCredential(loc, c)
}

// claudeCredentialsPath is where Claude Code keeps its credentials off macOS.
func claudeCredentialsPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

// writeCodexLogin replaces the tokens (and last_refresh) of Codex's auth.json.
// Other keys are left as Codex wrote them; an API-key login is switched to
// ChatGPT mode, since a ChatGPT login is the only kind this replaces.
func writeCodexLogin(creds json.RawMessage) error {
	var in struct {
		Tokens      map[string]any `json:"tokens"`
		LastRefresh any            `json:"last_refresh"`
	}
	if json.Unmarshal(creds, &in) != nil || in.Tokens == nil {
		return errors.New("the saved Codex login is unreadable")
	}
	home, _ := os.UserHomeDir()
	path := codexAuthFile(home)
	raw := map[string]any{}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &raw) != nil {
			return fmt.Errorf("%s is not valid JSON; not overwriting it", path)
		}
	}
	raw["tokens"] = in.Tokens
	if in.LastRefresh != nil {
		raw["last_refresh"] = in.LastRefresh
	}
	if m, _ := raw["auth_mode"].(string); m == "apikey" {
		raw["auth_mode"] = "chatgpt"
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}

// loginCommand is the vendor's own sign-in, which opens its login page and
// writes the new login where magpie reads it. A var so tests can fake it.
var loginCommand = func(agent string) (*exec.Cmd, error) {
	switch agent {
	case "claude":
		bin := claudeExecutable()
		if bin == "" {
			return nil, errors.New("Claude Code is not installed; install it, then run magpie accounts add claude again")
		}
		return exec.Command(bin, "auth", "login"), nil
	case "codex":
		bin, err := exec.LookPath("codex")
		if err != nil {
			return nil, errors.New("Codex is not installed; install it, then run magpie accounts add codex again")
		}
		return exec.Command(bin, "login"), nil
	}
	return nil, fmt.Errorf("unknown agent %q", agent)
}

// AddLogin signs the agent in through its vendor's login page, then remembers
// the account it ends up with. The account signed in before is saved first, so
// the new login does not replace it unrecorded.
func AddLogin(agent string) (string, error) {
	name, ok := LoginAgent(agent)
	if !ok {
		return "", fmt.Errorf("unknown agent %q", agent)
	}
	if err := RememberLogins(); err != nil {
		return "", err
	}
	cmd, err := loginCommand(name)
	if err != nil {
		return "", err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s login did not finish: %v", name, err)
	}
	forgetClaudeCredential()
	forgetClaudeStatus()
	if err := RememberLogins(); err != nil {
		return "", err
	}
	cur, ok := currentLogin(name)
	if !ok {
		return "", fmt.Errorf("%s login finished, but no account with an email was found", name)
	}
	return cur.User, nil
}

// storedClaudeToken returns a usable access token for a remembered Claude
// login, refreshing it through Anthropic when it is about to expire. The
// rotated pair is saved back to that login's record.
func storedClaudeToken(ctx context.Context, user string) (string, error) {
	var creds json.RawMessage
	for _, l := range loadLogins().Logins {
		if l.Agent == "claude" && strings.EqualFold(l.User, user) {
			creds = l.Creds
		}
	}
	if creds == nil {
		return "", fmt.Errorf("Claude account %s is no longer remembered", user)
	}
	var a claudeAuth
	if json.Unmarshal(creds, &a) != nil || a.AccessToken == "" {
		return "", fmt.Errorf("Claude account %s is signed out; run magpie accounts add claude", user)
	}
	if claudeFresh(claudeCredentials{OAuth: a}) {
		return a.AccessToken, nil
	}
	if a.RefreshToken == "" {
		return "", fmt.Errorf("Claude account %s token expired; sign in to it again", user)
	}
	fresh, err := refreshClaudeToken(ctx, a.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("Claude account %s is signed out (token refresh failed); sign in to it again", user)
	}
	var m map[string]any
	json.Unmarshal(creds, &m)
	if m == nil {
		m = map[string]any{}
	}
	m["accessToken"] = fresh.AccessToken
	if fresh.RefreshToken != "" {
		m["refreshToken"] = fresh.RefreshToken
	}
	if fresh.ExpiresIn > 0 {
		m["expiresAt"] = time.Now().Add(time.Duration(fresh.ExpiresIn) * time.Second).UnixMilli()
	}
	next, _ := json.Marshal(m)
	err = updateLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f.Logins, "claude", user)
		if i < 0 {
			return false, nil
		}
		f.Logins[i].Creds = compactJSON(next)
		return true, nil
	})
	return fresh.AccessToken, err
}

type oauthRefresh struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	IDToken      string `json:"id_token"`
}

// codexTokens is the "tokens" block of a stored Codex login.
type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

func refreshClaudeToken(ctx context.Context, refresh string) (oauthRefresh, error) {
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh, "client_id": claudeClientID})
	return postRefresh(ctx, claudeTokenURL, body)
}

func refreshCodexToken(ctx context.Context, refresh string) (oauthRefresh, error) {
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": refresh, "scope": "openid profile email"})
	return postRefresh(ctx, codexTokenURL, body)
}

func postRefresh(ctx context.Context, url string, body []byte) (oauthRefresh, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return oauthRefresh{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return oauthRefresh{}, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out oauthRefresh
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &out) != nil || out.AccessToken == "" {
		return oauthRefresh{}, fmt.Errorf("token refresh: %s", res.Status)
	}
	return out, nil
}

// storedCodexToken returns a usable access token and account id for a
// remembered Codex login, refreshing it when needed and saving the rotated
// tokens back to that login's record.
func storedCodexToken(ctx context.Context, user string) (string, string, error) {
	var creds json.RawMessage
	for _, l := range loadLogins().Logins {
		if l.Agent == "codex" && strings.EqualFold(l.User, user) {
			creds = l.Creds
		}
	}
	if creds == nil {
		return "", "", fmt.Errorf("ChatGPT account %s is no longer remembered", user)
	}
	var in struct {
		Tokens codexTokens `json:"tokens"`
	}
	if json.Unmarshal(creds, &in) != nil || in.Tokens.AccessToken == "" {
		return "", "", fmt.Errorf("ChatGPT account %s is signed out; sign in to it again", user)
	}
	t := in.Tokens
	accountID := t.AccountID
	if accountID == "" {
		accountID = claimString(jwtClaims(t.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(t.AccessToken)["exp"].(float64); exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute {
		return t.AccessToken, accountID, nil
	}
	if t.RefreshToken == "" {
		return "", "", fmt.Errorf("ChatGPT account %s token expired; sign in to it again", user)
	}
	fresh, err := refreshCodexToken(ctx, t.RefreshToken)
	if err != nil {
		return "", "", fmt.Errorf("ChatGPT account %s is signed out (token refresh failed); sign in to it again", user)
	}
	var m map[string]any
	json.Unmarshal(creds, &m)
	toks, _ := m["tokens"].(map[string]any)
	if toks == nil {
		toks = map[string]any{}
	}
	toks["access_token"] = fresh.AccessToken
	if fresh.IDToken != "" {
		toks["id_token"] = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		toks["refresh_token"] = fresh.RefreshToken
	}
	m["tokens"] = toks
	m["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
	next, _ := json.Marshal(m)
	err = updateLogins(func(f *loginsFile) (bool, error) {
		i := findLogin(f.Logins, "codex", user)
		if i < 0 {
			return false, nil
		}
		f.Logins[i].Creds = compactJSON(next)
		return true, nil
	})
	return fresh.AccessToken, accountID, err
}

// loginAccount makes the Account a remembered login signs requests with. It
// keeps the login's own tokens, so it never touches the agent's files.
func loginAccount(l savedLogin) *Account {
	user := l.User
	switch l.Agent {
	case "claude":
		acct := &Account{Agent: "claude", User: user, Plan: l.Plan}
		acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
			tok, err := storedClaudeToken(ctx, user)
			if err != nil {
				return err
			}
			setClaudeHeaders(req, tok, body)
			return nil
		}
		acct.body = claudeBody
		acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
		acct.fetch = func(context.Context) ([]catalog.Model, error) {
			return nil, errors.New("refresh the models from the account the agent is signed in as")
		}
		// the agent's own binary runs on this login's access token
		acct.env = func(ctx context.Context) ([]string, error) {
			tok, err := storedClaudeToken(ctx, user)
			if err != nil {
				return nil, err
			}
			return []string{"CLAUDE_CODE_OAUTH_TOKEN=" + tok}, nil
		}
		return acct
	case "codex":
		acct := &Account{Agent: "codex", User: user, Plan: l.Plan, Stream: true}
		acct.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
			tok, accountID, err := storedCodexToken(ctx, user)
			if err != nil {
				return err
			}
			setCodexHeaders(req, tok, accountID)
			return nil
		}
		acct.body = codexBody
		acct.models = catalog.Codex
		acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
		return acct
	}
	return nil
}

// attachLogins gives a signed-in subscription provider the switched-on logins
// that come after it, so the gateway can fall back to them.
func attachLogins(p Provider) Provider {
	if p.Account == nil {
		return p
	}
	for _, l := range otherOnLogins(p.Account.Agent, p.Account.User) {
		if a := loginAccount(l); a != nil {
			p.Account.alts = append(p.Account.alts, a)
		}
	}
	return p
}

// AccountViews lists the ways to reach a subscription provider: the login the
// agent is signed in as first, then the switched-on logins in saved order. Each
// is the provider itself, signed with that login's credentials.
func (p Provider) AccountViews() []Provider {
	if p.Account == nil {
		return []Provider{p}
	}
	out := []Provider{p}
	for _, a := range p.Account.alts {
		v := p
		v.Account = a
		out = append(out, v)
	}
	return out
}

// Env is the extra environment the agent's own binary needs to run as this
// login. It is nil for the login the agent is signed in as.
func (a *Account) Env(ctx context.Context) ([]string, error) {
	if a == nil || a.env == nil {
		return nil, nil
	}
	return a.env(ctx)
}

// setClaudeHeaders signs a request to Anthropic as a Claude Code subscription.
func setClaudeHeaders(req *http.Request, tok string, body []byte) {
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("User-Agent", claudeUserAgent())
	req.Header.Set("X-Claude-Code-Session-Id", claudeSessionID())
	req.Header.Set("anthropic-beta", claudeBetaHeader(claudeModelOf(body)))
	req.Header.Set("anthropic-dangerous-direct-browser-access", "true")
	req.Header.Set("x-app", "cli")
	req.Header.Set("x-client-request-id", randomUUID())
	for k, v := range claudeStainlessHeaders() {
		req.Header.Set(k, v)
	}
}

// setCodexHeaders signs a request to the ChatGPT backend as a Codex login.
func setCodexHeaders(req *http.Request, tok, accountID string) {
	req.Header.Set("Authorization", "Bearer "+tok)
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "magpie")
}
