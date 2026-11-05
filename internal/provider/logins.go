package provider

// Remembered sign-ins: the Claude Code and Codex accounts magpie has seen
// signed in, so the user can switch between them, forget them, and let more
// than one serve at once. They live in accounts.json beside providers.json,
// readable by the user only.
//
// A refresh token has one holder at a time. The account the agent is signed
// in as keeps its tokens in the agent's own store and has no copy here. Every
// other remembered account keeps its tokens in its record (Login), and magpie
// refreshes those itself. Switching moves tokens between the two places.

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

// loginRecord is one remembered account.
type loginRecord struct {
	Agent string          `json:"agent"`
	User  string          `json:"user"`
	Plan  string          `json:"plan,omitempty"`
	On    bool            `json:"on,omitempty"`    // used by the gateway alongside the one signed in
	Login json.RawMessage `json:"login,omitempty"` // the account's tokens, only while it is not signed in
}

type loginFile struct {
	Logins []loginRecord `json:"logins"`
}

// Login is a remembered account as listed to the user.
type Login struct {
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Plan    string `json:"plan,omitempty"`
	Current bool   `json:"current"` // signed in to the agent now
	On      bool   `json:"on"`      // the gateway may use it alongside the current account
}

var (
	loginMu        sync.Mutex // guards accounts.json
	loginRefreshMu sync.Mutex // serializes refreshes of remembered accounts' tokens
)

// LoginAgent maps a name the user typed to an agent that has remembered
// sign-ins: claude (or cc) and codex.
func LoginAgent(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func loginAgentOf(name string) (string, error) {
	a, ok := LoginAgent(name)
	if !ok {
		return "", fmt.Errorf("%q has no remembered accounts; use claude (cc) or codex", name)
	}
	return a, nil
}

func loginName(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

func loginsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

func loadLogins() loginFile {
	var f loginFile
	if b, err := os.ReadFile(loginsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

// storeLogins writes accounts.json for the user only: it holds tokens.
func storeLogins(f loginFile) error {
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

func findLogin(f loginFile, agent, user string) int {
	for i, r := range f.Logins {
		if r.Agent == agent && strings.EqualFold(r.User, user) {
			return i
		}
	}
	return -1
}

func countLogins(f loginFile, agent string) int {
	n := 0
	for _, r := range f.Logins {
		if r.Agent == agent {
			n++
		}
	}
	return n
}

// signedIn is the account an agent is signed in as, with its tokens.
type signedIn struct {
	user, plan string
	login      json.RawMessage
}

// signedInAs reports the account the agent is signed in as. fresh reads the
// agent's own store again rather than the copy the Claude cache holds; it is
// used when tokens are about to move.
func signedInAs(agent string, fresh bool) (signedIn, bool) {
	switch agent {
	case "claude":
		acct, ok := claudeAccount()
		if !ok {
			return signedIn{}, false
		}
		var c claudeCredentials
		if fresh {
			c, _, ok = readClaudeCredential()
		} else {
			c, _, ok = claudeCredential()
		}
		if !ok {
			return signedIn{}, false
		}
		blob, err := json.Marshal(c.raw["claudeAiOauth"])
		if err != nil {
			return signedIn{}, false
		}
		return signedIn{user: acct.Account.User, plan: acct.Account.Plan, login: blob}, true
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return signedIn{}, false
		}
		acct, ok := codexAccount(home)
		if !ok {
			return signedIn{}, false
		}
		var raw map[string]any
		if !readJSON(codexAuthPath(home), &raw) {
			return signedIn{}, false
		}
		blob, err := json.Marshal(raw["tokens"])
		if err != nil {
			return signedIn{}, false
		}
		return signedIn{user: acct.Account.User, plan: acct.Account.Plan, login: blob}, true
	}
	return signedIn{}, false
}

func codexAuthPath(home string) string { return filepath.Join(home, ".codex", "auth.json") }

// claudeConfigDir is where Claude Code keeps its credentials file.
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// syncLogins remembers the account each agent is signed in as, and clears
// any tokens magpie held for it, since the agent now holds them. An account
// seen for the first time is remembered; the second and later ones are on
// by default. The file is written only when something changed.
func syncLogins() {
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	before, _ := json.Marshal(f)
	for _, agent := range []string{"claude", "codex"} {
		cur, ok := signedInAs(agent, false)
		if !ok || cur.user == "" {
			continue
		}
		i := findLogin(f, agent, cur.user)
		if i < 0 {
			f.Logins = append(f.Logins, loginRecord{Agent: agent, User: cur.user, Plan: cur.plan, On: countLogins(f, agent) > 0})
			continue
		}
		if cur.plan != "" {
			f.Logins[i].Plan = cur.plan
		}
		f.Logins[i].Login = nil
	}
	after, _ := json.Marshal(f)
	if !bytes.Equal(before, after) {
		storeLogins(f)
	}
}

// Logins lists an agent's remembered accounts in the order they were remembered.
func Logins(agent string) ([]Login, error) {
	agent, err := loginAgentOf(agent)
	if err != nil {
		return nil, err
	}
	syncLogins()
	cur, signed := signedInAs(agent, false)
	loginMu.Lock()
	f := loadLogins()
	loginMu.Unlock()
	out := []Login{}
	for _, r := range f.Logins {
		if r.Agent != agent {
			continue
		}
		l := Login{Agent: agent, User: r.User, Plan: r.Plan, On: r.On}
		if signed && strings.EqualFold(cur.user, r.User) {
			l.Current, l.On = true, true
		}
		out = append(out, l)
	}
	return out, nil
}

// SetLoginOn lets a remembered account serve alongside the one signed in, or
// stops it. The account signed in always serves, so it cannot be turned off.
func SetLoginOn(agent, user string, on bool) error {
	agent, err := loginAgentOf(agent)
	if err != nil {
		return err
	}
	syncLogins()
	cur, signed := signedInAs(agent, false)
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	i := findLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", loginName(agent), user)
	}
	if !on && signed && strings.EqualFold(cur.user, user) {
		return fmt.Errorf("%s is the %s account signed in now; it always stays on", user, loginName(agent))
	}
	f.Logins[i].On = on
	return storeLogins(f)
}

// SwitchLogin signs the agent in as a remembered account. The account it
// replaces keeps its latest tokens and inherits the replaced account's
// on state, which the new one held before. Nothing else in the agent's own
// files changes.
func SwitchLogin(agent, user string) error {
	agent, err := loginAgentOf(agent)
	if err != nil {
		return err
	}
	syncLogins()
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	i := findLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q; magpie accounts lists them", loginName(agent), user)
	}
	target := f.Logins[i]
	cur, signed := signedInAs(agent, true)
	if signed && strings.EqualFold(cur.user, target.User) {
		return nil
	}
	if len(target.Login) == 0 {
		return fmt.Errorf("%s is signed out of %s; run magpie accounts add %s to sign in again", target.User, loginName(agent), agent)
	}
	if signed {
		j := findLogin(f, agent, cur.user)
		if j < 0 {
			f.Logins = append(f.Logins, loginRecord{Agent: agent, User: cur.user})
			j = len(f.Logins) - 1
		}
		f.Logins[j].Login = cur.login
		if cur.plan != "" {
			f.Logins[j].Plan = cur.plan
		}
		f.Logins[j].On = target.On
	}
	if err := writeLogin(agent, target.Login); err != nil {
		return err
	}
	f.Logins[i].Login = nil
	forgetLoginCaches(agent)
	return storeLogins(f)
}

// ForgetLogin drops a remembered account. The account signed in cannot be
// forgotten: sign out in the agent first.
func ForgetLogin(agent, user string) error {
	agent, err := loginAgentOf(agent)
	if err != nil {
		return err
	}
	syncLogins()
	cur, signed := signedInAs(agent, false)
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	i := findLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", loginName(agent), user)
	}
	if signed && strings.EqualFold(cur.user, user) {
		return fmt.Errorf("%s is the %s account signed in now; sign out in %s first", user, loginName(agent), loginName(agent))
	}
	f.Logins = append(f.Logins[:i], f.Logins[i+1:]...)
	return storeLogins(f)
}

// StartLogin returns the command that signs the agent in to an account,
// which opens the vendor's sign-in page. The account signed in now keeps its
// tokens in its record while that runs, so they are not lost when the agent
// replaces them. Call FinishLogin once the command has exited.
func StartLogin(agent string) (*exec.Cmd, error) {
	agent, err := loginAgentOf(agent)
	if err != nil {
		return nil, err
	}
	var cmd *exec.Cmd
	switch agent {
	case "claude":
		path := claudeExecutable()
		if path == "" {
			return nil, errors.New("Claude Code is not installed")
		}
		cmd = exec.Command(path, "auth", "login")
	case "codex":
		path, err := exec.LookPath("codex")
		if err != nil {
			return nil, errors.New("Codex is not installed")
		}
		cmd = exec.Command(path, "login")
	}
	syncLogins()
	if cur, ok := signedInAs(agent, true); ok {
		loginMu.Lock()
		f := loadLogins()
		if i := findLogin(f, agent, cur.user); i >= 0 {
			f.Logins[i].Login = cur.login
			storeLogins(f)
		}
		loginMu.Unlock()
	}
	return cmd, nil
}

// FinishLogin remembers the account the agent is signed in as after the
// command from StartLogin has run, and returns it.
func FinishLogin(agent string) (Login, error) {
	agent, err := loginAgentOf(agent)
	if err != nil {
		return Login{}, err
	}
	forgetLoginCaches(agent)
	syncLogins()
	logins, _ := Logins(agent)
	for _, l := range logins {
		if l.Current {
			return l, nil
		}
	}
	return Login{}, fmt.Errorf("%s is not signed in", loginName(agent))
}

// forgetLoginCaches drops what was read from an agent's store before a switch.
func forgetLoginCaches(agent string) {
	if agent == "claude" {
		forgetClaudeCredential()
		forgetClaudeStatus()
	}
}

// writeLogin puts a remembered account's tokens into the agent's own store,
// keeping every other field the store holds.
func writeLogin(agent string, login json.RawMessage) error {
	switch agent {
	case "claude":
		var oauth map[string]any
		if err := json.Unmarshal(login, &oauth); err != nil || oauth == nil {
			return errors.New("the saved Claude Code sign-in is damaged")
		}
		c, loc, ok := readClaudeCredential()
		if !ok {
			c = claudeCredentials{raw: map[string]any{}}
			loc = claudeCredentialLocation{path: filepath.Join(claudeConfigDir(), ".credentials.json")}
			if claudeKeychain {
				loc = claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
			}
		}
		if c.raw == nil {
			c.raw = map[string]any{}
		}
		c.raw["claudeAiOauth"] = oauth
		json.Unmarshal(login, &c.OAuth)
		if !loc.keychain {
			if err := os.MkdirAll(filepath.Dir(loc.path), 0o700); err != nil {
				return err
			}
		}
		return saveClaudeCredential(loc, c)
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		var tokens map[string]any
		if err := json.Unmarshal(login, &tokens); err != nil || tokens == nil {
			return errors.New("the saved Codex sign-in is damaged")
		}
		path := codexAuthPath(home)
		var raw map[string]any
		if !readJSON(path, &raw) || raw == nil {
			raw = map[string]any{}
		}
		raw["auth_mode"] = "chatgpt"
		raw["tokens"] = tokens
		raw["last_refresh"] = time.Now().UTC().Format(time.RFC3339Nano)
		b, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		return os.WriteFile(path, append(b, '\n'), 0o600)
	}
	return fmt.Errorf("%q has no remembered accounts", agent)
}

// updateLogin stores new tokens for a remembered account that is not signed in.
func updateLogin(agent, user string, login json.RawMessage) error {
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	i := findLogin(f, agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", loginName(agent), user)
	}
	f.Logins[i].Login = login
	return storeLogins(f)
}

// savedLogin is a remembered account's tokens, if it holds any.
func savedLogin(agent, user string) (json.RawMessage, bool) {
	loginMu.Lock()
	defer loginMu.Unlock()
	f := loadLogins()
	i := findLogin(f, agent, user)
	if i < 0 || len(f.Logins[i].Login) == 0 {
		return nil, false
	}
	return f.Logins[i].Login, true
}

// savedClaudeToken returns a usable access token for a remembered Claude
// account, refreshing it through Anthropic when it is about to expire.
func savedClaudeToken(ctx context.Context, user string) (string, error) {
	loginRefreshMu.Lock()
	defer loginRefreshMu.Unlock()
	login, ok := savedLogin("claude", user)
	if !ok {
		return "", fmt.Errorf("%s is signed out of Claude Code; run magpie accounts add claude", user)
	}
	var raw map[string]any
	var oauth claudeAuth
	if json.Unmarshal(login, &raw) != nil || json.Unmarshal(login, &oauth) != nil {
		return "", errors.New("the saved Claude Code sign-in is damaged")
	}
	if claudeFresh(claudeCredentials{OAuth: oauth}) {
		return oauth.AccessToken, nil
	}
	if oauth.RefreshToken == "" {
		return "", errors.New("Claude Code OAuth token expired; run magpie accounts add claude")
	}
	access, refresh, expiresIn, err := refreshClaude(ctx, oauth.RefreshToken)
	if err != nil {
		return "", err
	}
	raw["accessToken"], raw["expiresAt"] = access, time.Now().Add(time.Duration(expiresIn)*time.Second).UnixMilli()
	if refresh != "" {
		raw["refreshToken"] = refresh
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}
	if err := updateLogin("claude", user, b); err != nil {
		return "", err
	}
	return access, nil
}

// refreshClaude trades a refresh token for a new access token.
func refreshClaude(ctx context.Context, refresh string) (access, next string, expiresIn int64, err error) {
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": refresh, "client_id": claudeClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", 0, errors.New("Claude Code token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return "", "", 0, errors.New("Claude Code is signed out (token refresh failed); run magpie accounts add claude")
	}
	return fresh.AccessToken, fresh.RefreshToken, fresh.ExpiresIn, nil
}

// savedCodexToken returns a usable access token and account id for a
// remembered Codex account, refreshing it through OpenAI when needed.
func savedCodexToken(ctx context.Context, user string) (tok, accountID string, err error) {
	loginRefreshMu.Lock()
	defer loginRefreshMu.Unlock()
	login, ok := savedLogin("codex", user)
	if !ok {
		return "", "", fmt.Errorf("%s is signed out of Codex; run magpie accounts add codex", user)
	}
	var raw map[string]any
	if json.Unmarshal(login, &raw) != nil || raw == nil {
		return "", "", errors.New("the saved Codex sign-in is damaged")
	}
	access, _ := raw["access_token"].(string)
	refresh, _ := raw["refresh_token"].(string)
	accountID, _ = raw["account_id"].(string)
	if accountID == "" {
		idTok, _ := raw["id_token"].(string)
		accountID = claimString(jwtClaims(idTok), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(access)["exp"].(float64); access != "" && (exp == 0 || time.Until(time.Unix(int64(exp), 0)) > 5*time.Minute) {
		return access, accountID, nil
	}
	if refresh == "" {
		return "", "", errors.New("Codex token expired; run magpie accounts add codex")
	}
	fresh, err := refreshCodex(ctx, refresh)
	if err != nil {
		return "", "", err
	}
	raw["access_token"] = fresh.AccessToken
	if fresh.IDToken != "" {
		raw["id_token"] = fresh.IDToken
	}
	if fresh.RefreshToken != "" {
		raw["refresh_token"] = fresh.RefreshToken
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", "", err
	}
	if err := updateLogin("codex", user, b); err != nil {
		return "", "", err
	}
	return fresh.AccessToken, accountID, nil
}

// refreshCodex trades a refresh token for new Codex tokens.
func refreshCodex(ctx context.Context, refresh string) (struct{ IDToken, AccessToken, RefreshToken string }, error) {
	var out struct{ IDToken, AccessToken, RefreshToken string }
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": refresh, "scope": "openid profile email"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return out, errors.New("Codex token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return out, errors.New("Codex is signed out (token refresh failed); run magpie accounts add codex")
	}
	out.IDToken, out.AccessToken, out.RefreshToken = fresh.IDToken, fresh.AccessToken, fresh.RefreshToken
	return out, nil
}

// savedCodexProvider is a remembered Codex account, as a provider the gateway
// can send requests through with that account's own tokens.
func savedCodexProvider(user, plan string) Provider {
	acct := &Account{Agent: "codex", Stream: true, User: user, Plan: plan}
	acct.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
		tok, accountID, err := savedCodexToken(ctx, user)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if accountID != "" {
			req.Header.Set("chatgpt-account-id", accountID)
		}
		req.Header.Set("OpenAI-Beta", "responses=experimental")
		req.Header.Set("originator", "magpie")
		return nil
	}
	acct.body = codexBody
	acct.models = catalog.Codex
	acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
	return Provider{ID: "codex", Name: "Codex", Icon: "codex-color", Responses: CodexBase, Website: "https://chatgpt.com/codex", Account: acct}
}

// codexPool lists the remembered Codex accounts the gateway may use besides
// the one signed in: the ones switched on that hold tokens of their own, in
// the order they were remembered.
func codexPool(current string) []Provider {
	loginMu.Lock()
	f := loadLogins()
	loginMu.Unlock()
	var out []Provider
	for _, r := range f.Logins {
		if r.Agent == "codex" && r.On && len(r.Login) > 0 && !strings.EqualFold(r.User, current) {
			out = append(out, savedCodexProvider(r.User, r.Plan))
		}
	}
	return out
}
