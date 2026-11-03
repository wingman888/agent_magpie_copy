package provider

// Remembered sign-ins: every Claude Code and Codex account the user has
// signed in to, kept so magpie can switch between them and let several work
// at once.
//
// One holder per refresh token. The agent keeps the sign-in it is using in its
// own credential file, and only the agent refreshes that one. magpie keeps the
// other remembered accounts' tokens in accounts.json (mode 0600, beside
// providers.json), and only magpie refreshes those, and only while the account
// works alongside the agent's sign-in (see Account.also). Switching hands the
// replaced sign-in's latest tokens to the store before the agent's file is
// rewritten, so nothing is lost on the way.

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

// claudeWhoAmI says who Claude Code is signed in as; a var so tests can stand
// in for `claude auth status`.
var claudeWhoAmI = claudeIdentity

// AgentAccount is one remembered sign-in, as the accounts listing shows it.
type AgentAccount struct {
	Agent   string `json:"agent"`
	User    string `json:"user"`
	Plan    string `json:"plan,omitempty"`
	Current bool   `json:"current"` // the agent is signed in as this account
	On      bool   `json:"on"`      // works alongside the sign-in in use; the one in use always does
}

// savedAccount is what accounts.json keeps for one remembered sign-in.
type savedAccount struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	On    bool   `json:"on,omitempty"`
	// Creds is the agent's own token block (Claude Code's OAuth object, or
	// Codex's tokens). Empty while the agent is signed in as this account.
	Creds json.RawMessage `json:"creds,omitempty"`
}

type accountsFile struct {
	Accounts []savedAccount `json:"accounts"`
}

// accountsMu guards accounts.json and serializes refreshes of remembered tokens.
var accountsMu sync.Mutex

func accountsPath() string { return filepath.Join(filepath.Dir(Path()), "accounts.json") }

// loadAccounts reads the store; callers hold accountsMu.
func loadAccounts() accountsFile {
	var f accountsFile
	if b, err := os.ReadFile(accountsPath()); err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

// saveAccounts writes the store; callers hold accountsMu.
func saveAccounts(f accountsFile) error {
	p := accountsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(p, append(b, '\n'))
}

// writeSecret replaces path with data, readable and writable by its owner only.
func writeSecret(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// sameUser compares sign-in names (emails) without regard to case.
func sameUser(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func findAccount(f accountsFile, agent, user string) int {
	for i, s := range f.Accounts {
		if s.Agent == agent && sameUser(s.User, user) {
			return i
		}
	}
	return -1
}

func countAgent(f accountsFile, agent string) int {
	n := 0
	for _, s := range f.Accounts {
		if s.Agent == agent {
			n++
		}
	}
	return n
}

// AccountAgent turns an agent name, or its alias, into the id magpie keeps
// sign-ins under. Only Claude Code (claude, cc) and Codex have them.
func AccountAgent(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc":
		return "claude", nil
	case "codex":
		return "codex", nil
	}
	return "", fmt.Errorf("accounts work with claude (cc) and codex, not %q", name)
}

func agentLabel(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

func unknownAccount(agent, user string) error {
	return fmt.Errorf("magpie has no %s account %s; magpie accounts %s lists the remembered ones",
		agentLabel(agent), strings.TrimSpace(user), agent)
}

// currentSignIn is the account the agent is signed in as right now.
func currentSignIn(agent string) (user, plan string, ok bool) {
	switch agent {
	case "claude":
		email, _, signedOut := claudeWhoAmI()
		if signedOut || strings.TrimSpace(email) == "" {
			return "", "", false
		}
		p, ok := claudeAccount()
		if !ok {
			return "", "", false
		}
		return strings.TrimSpace(email), p.Account.Plan, true
	case "codex":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", false
		}
		p, ok := codexAccount(home)
		if !ok || p.Account.User == "ChatGPT" { // no email on the sign-in: nothing to remember it by
			return "", "", false
		}
		return p.Account.User, p.Account.Plan, true
	}
	return "", "", false
}

// codexAuthPath is Codex's own sign-in file.
func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// readRawMap reads a JSON object keeping every field; a missing file is empty.
func readRawMap(path string) map[string]any {
	var m map[string]any
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m
}

// agentCreds is the token block the agent itself keeps for its sign-in.
func agentCreds(agent string) (json.RawMessage, bool) {
	var v any
	switch agent {
	case "claude":
		c, _, ok := readClaudeCredential()
		if !ok {
			return nil, false
		}
		v = c.raw["claudeAiOauth"]
	case "codex":
		v = readRawMap(codexAuthPath())["tokens"]
	}
	if v == nil {
		return nil, false
	}
	b, err := json.Marshal(v)
	return b, err == nil
}

// writeAgentCreds signs the agent in with a token block. Every other field of
// the agent's own file stays as it was.
func writeAgentCreds(agent string, block json.RawMessage) error {
	var v map[string]any
	if err := json.Unmarshal(block, &v); err != nil || v == nil {
		return fmt.Errorf("the saved %s sign-in is unreadable; magpie accounts add %s", agentLabel(agent), agent)
	}
	switch agent {
	case "claude":
		c, loc, ok := readClaudeCredential()
		if !ok {
			// signed out: keep what else the file holds, such as MCP state
			loc = claudeCredentialLocation{path: claudeCredentialPath(), keychain: claudeKeychain, account: os.Getenv("USER")}
			c = claudeCredentials{raw: map[string]any{}}
			if !claudeKeychain {
				c.raw = readRawMap(loc.path)
			}
		}
		if c.raw == nil {
			c.raw = map[string]any{}
		}
		c.raw["claudeAiOauth"] = v
		var a claudeAuth
		json.Unmarshal(block, &a)
		c.OAuth = a
		if !loc.keychain {
			if err := os.MkdirAll(filepath.Dir(loc.path), 0o700); err != nil {
				return err
			}
		}
		if err := saveClaudeCredential(loc, c); err != nil {
			return err
		}
		forgetClaudeStatus()
		return nil
	case "codex":
		path := codexAuthPath()
		raw := readRawMap(path)
		raw["tokens"] = v
		raw["auth_mode"] = "chatgpt" // an API-key sign-in would hide the account
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		b, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		return writeSecret(path, append(b, '\n'))
	}
	return fmt.Errorf("no sign-in files known for %q", agent)
}

// rememberCurrent records the account the agent is signed in as, the first time
// magpie sees it. The agent keeps its tokens, so any stored copy is dropped.
func rememberCurrent(agent string) {
	user, plan, ok := currentSignIn(agent)
	if !ok {
		return
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	changed := false
	i := findAccount(f, agent, user)
	if i < 0 {
		// a second account works alongside the first from the start
		f.Accounts = append(f.Accounts, savedAccount{Agent: agent, User: user, On: countAgent(f, agent) > 0})
		i, changed = len(f.Accounts)-1, true
	}
	s := &f.Accounts[i]
	if plan != "" && s.Plan != plan {
		s.Plan, changed = plan, true
	}
	if len(s.Creds) > 0 {
		s.Creds, changed = nil, true
	}
	if changed {
		saveAccounts(f)
	}
}

// keepCurrent copies the agent's own tokens into the store, so the sign-in that
// is about to be replaced is not lost. rememberCurrent drops the copy again
// once the agent is known to hold the tokens.
func keepCurrent(agent string) {
	user, _, ok := currentSignIn(agent)
	if !ok {
		return
	}
	latest, ok := agentCreds(agent)
	if !ok {
		return
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	if i := findAccount(f, agent, user); i >= 0 {
		f.Accounts[i].Creds = latest
		saveAccounts(f)
	}
}

// ListAccounts lists the remembered sign-ins of an agent, noting first the one
// the agent is signed in as now.
func ListAccounts(agent string) ([]AgentAccount, error) {
	agent, err := AccountAgent(agent)
	if err != nil {
		return nil, err
	}
	rememberCurrent(agent)
	cur, _, hasCur := currentSignIn(agent)
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	var out []AgentAccount
	for _, s := range f.Accounts {
		if s.Agent != agent {
			continue
		}
		current := hasCur && sameUser(cur, s.User)
		out = append(out, AgentAccount{Agent: agent, User: s.User, Plan: s.Plan, Current: current, On: current || s.On})
	}
	return out, nil
}

// SwitchAccount signs the agent in as a remembered account. The sign-in it
// replaces keeps its latest tokens, and it takes over the target's "works
// alongside" state. Sessions already running keep the old account until they
// restart. It reports whether the agent's sign-in changed.
func SwitchAccount(agent, user string) (bool, error) {
	agent, err := AccountAgent(agent)
	if err != nil {
		return false, err
	}
	rememberCurrent(agent)
	cur, _, hasCur := currentSignIn(agent)
	latest, hasLatest := agentCreds(agent)

	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	ti := findAccount(f, agent, user)
	if ti < 0 {
		return false, unknownAccount(agent, user)
	}
	if hasCur && sameUser(cur, user) {
		return false, nil
	}
	target := f.Accounts[ti]
	if len(target.Creds) == 0 {
		return false, fmt.Errorf("magpie holds no %s sign-in for %s; magpie accounts add %s and sign in as them",
			agentLabel(agent), target.User, agent)
	}
	if hasCur {
		ci := findAccount(f, agent, cur)
		if ci < 0 || !hasLatest {
			return false, fmt.Errorf("%s is signed in as %s, which magpie has not remembered; not switching",
				agentLabel(agent), cur)
		}
		f.Accounts[ci].Creds = latest
		f.Accounts[ci].On = target.On
	}
	// the replaced sign-in is kept before the agent's own file changes
	if err := saveAccounts(f); err != nil {
		return false, err
	}
	if err := writeAgentCreds(agent, target.Creds); err != nil {
		return false, err
	}
	// the agent holds the target's tokens now; the store does not
	f.Accounts[ti].Creds = nil
	return true, saveAccounts(f)
}

// ForgetAccount drops a remembered sign-in. The one the agent is signed in as
// cannot be forgotten: sign out or switch first.
func ForgetAccount(agent, user string) error {
	agent, err := AccountAgent(agent)
	if err != nil {
		return err
	}
	if cur, _, ok := currentSignIn(agent); ok && sameUser(cur, user) {
		return fmt.Errorf("%s is the %s sign-in in use; switch to another account before forgetting it",
			cur, agentLabel(agent))
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := findAccount(f, agent, user)
	if i < 0 {
		return unknownAccount(agent, user)
	}
	f.Accounts = append(f.Accounts[:i], f.Accounts[i+1:]...)
	return saveAccounts(f)
}

// SetLoginOn lets a remembered account work alongside the agent's sign-in, or
// stops it. The sign-in in use always works, so turning it off is refused.
func SetLoginOn(agent, user string, on bool) error {
	agent, err := AccountAgent(agent)
	if err != nil {
		return err
	}
	rememberCurrent(agent)
	cur, _, hasCur := currentSignIn(agent)
	if hasCur && sameUser(cur, user) && !on {
		return fmt.Errorf("%s is the %s sign-in in use; it always works and cannot be turned off",
			cur, agentLabel(agent))
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := findAccount(f, agent, user)
	if i < 0 {
		return unknownAccount(agent, user)
	}
	f.Accounts[i].On = on
	return saveAccounts(f)
}

// agentLogin runs the agent's own sign-in: it opens the vendor's page and writes
// the new sign-in where the agent keeps it. A var so tests can stand in for the
// browser.
var agentLogin = func(agent string) error {
	var bin string
	var args []string
	switch agent {
	case "claude":
		bin = claudeExecutable()
		args = []string{"auth", "login"}
	case "codex":
		bin, _ = exec.LookPath("codex")
		args = []string{"login"}
	}
	if bin == "" {
		return fmt.Errorf("%s is not installed; install it, then try again", agentLabel(agent))
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// AddAccount runs the agent's sign-in and remembers the account it ends up in.
// The sign-in it replaces is kept, and the second and later accounts work
// alongside the first from the start.
func AddAccount(agent string) (string, error) {
	agent, err := AccountAgent(agent)
	if err != nil {
		return "", err
	}
	rememberCurrent(agent)
	keepCurrent(agent)
	loginErr := agentLogin(agent)
	forgetClaudeCredential()
	forgetClaudeStatus()
	rememberCurrent(agent) // also undoes keepCurrent if the sign-in did not change
	user, _, ok := currentSignIn(agent)
	if loginErr != nil {
		return "", fmt.Errorf("%s sign-in did not finish: %v", agentLabel(agent), loginErr)
	}
	if !ok {
		return "", fmt.Errorf("%s is not signed in after the sign-in", agentLabel(agent))
	}
	return user, nil
}

// savedAlso are the remembered accounts that work alongside the agent's
// sign-in, in the order they were remembered.
func savedAlso(agent, current string) []*Account {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	var out []*Account
	for _, s := range f.Accounts {
		if s.Agent != agent || !s.On || len(s.Creds) == 0 || sameUser(s.User, current) {
			continue
		}
		if agent == "codex" {
			out = append(out, savedCodexAccount(s.User, s.Plan))
		} else {
			out = append(out, savedClaudeAccount(s.User, s.Plan))
		}
	}
	return out
}

func savedClaudeAccount(user, plan string) *Account {
	acct := &Account{Agent: "claude", User: user, Plan: plan}
	acct.sign = func(ctx context.Context, req *http.Request, body []byte) error {
		tok, err := savedClaudeToken(ctx, user)
		if err != nil {
			return err
		}
		applyClaudeHeaders(req, tok, body)
		return nil
	}
	acct.body = claudeBody
	acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
	return acct
}

// savedClaudeToken returns a usable token for a remembered Claude account,
// refreshing it here when it is about to expire: the agent is not signed in as
// it, so nothing else would.
func savedClaudeToken(ctx context.Context, user string) (string, error) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := findAccount(f, "claude", user)
	if i < 0 || len(f.Accounts[i].Creds) == 0 {
		return "", fmt.Errorf("no saved Claude Code sign-in for %s; magpie accounts add claude", user)
	}
	var block map[string]any
	var a claudeAuth
	if json.Unmarshal(f.Accounts[i].Creds, &block) != nil || json.Unmarshal(f.Accounts[i].Creds, &a) != nil {
		return "", errors.New("the saved Claude Code sign-in is unreadable; magpie accounts add claude")
	}
	if claudeFresh(claudeCredentials{OAuth: a}) {
		return a.AccessToken, nil
	}
	if err := refreshClaudeAuth(ctx, &a); err != nil {
		return "", err
	}
	b, err := json.Marshal(setClaudeOAuth(block, a))
	if err != nil {
		return "", err
	}
	f.Accounts[i].Creds = b
	if err := saveAccounts(f); err != nil {
		return "", err
	}
	return a.AccessToken, nil
}

func savedCodexAccount(user, plan string) *Account {
	acct := &Account{Agent: "codex", Stream: true, User: user, Plan: plan}
	acct.sign = func(ctx context.Context, req *http.Request, _ []byte) error {
		tok, accountID, err := savedCodexToken(ctx, user)
		if err != nil {
			return err
		}
		applyCodexHeaders(req, tok, accountID)
		return nil
	}
	acct.body = codexBody
	acct.models = catalog.Codex
	acct.fetch = func(context.Context) ([]catalog.Model, error) { return catalog.Codex(), nil }
	return acct
}

// savedCodexToken returns a usable access token and account id for a remembered
// Codex account, refreshing it here when it is about to expire.
func savedCodexToken(ctx context.Context, user string) (string, string, error) {
	accountsMu.Lock()
	defer accountsMu.Unlock()
	f := loadAccounts()
	i := findAccount(f, "codex", user)
	if i < 0 || len(f.Accounts[i].Creds) == 0 {
		return "", "", fmt.Errorf("no saved Codex sign-in for %s; magpie accounts add codex", user)
	}
	var tokens map[string]any
	if json.Unmarshal(f.Accounts[i].Creds, &tokens) != nil || tokens == nil {
		return "", "", errors.New("the saved Codex sign-in is unreadable; magpie accounts add codex")
	}
	access, _ := tokens["access_token"].(string)
	refresh, _ := tokens["refresh_token"].(string)
	accountID, _ := tokens["account_id"].(string)
	if accountID == "" {
		idTok, _ := tokens["id_token"].(string)
		accountID = claimString(jwtClaims(idTok), "https://api.openai.com/auth", "chatgpt_account_id")
	}
	if exp, _ := jwtClaims(access)["exp"].(float64); exp != 0 && time.Until(time.Unix(int64(exp), 0)) <= 5*time.Minute {
		idTok, newAccess, newRefresh, err := refreshCodex(ctx, refresh)
		if err != nil {
			return "", "", err
		}
		access = newAccess
		tokens["access_token"] = newAccess
		if idTok != "" {
			tokens["id_token"] = idTok
		}
		if newRefresh != "" {
			tokens["refresh_token"] = newRefresh
		}
		b, err := json.Marshal(tokens)
		if err != nil {
			return "", "", err
		}
		f.Accounts[i].Creds = b
		if err := saveAccounts(f); err != nil {
			return "", "", err
		}
	}
	return access, accountID, nil
}

// refreshClaudeAuth trades a Claude refresh token for a new access token, in
// place. The refresh token rotates, so the new one replaces the old.
func refreshClaudeAuth(ctx context.Context, a *claudeAuth) error {
	if a.RefreshToken == "" {
		return errors.New("Claude Code OAuth token expired; run claude auth login")
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": a.RefreshToken,
		"client_id": claudeClientID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeTokenURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.New("Claude Code token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return errors.New("Claude Code is signed out (token refresh failed); run claude auth login")
	}
	a.AccessToken = fresh.AccessToken
	if fresh.RefreshToken != "" {
		a.RefreshToken = fresh.RefreshToken
	}
	if fresh.ExpiresIn > 0 {
		a.ExpiresAt = time.Now().Add(time.Duration(fresh.ExpiresIn) * time.Second).UnixMilli()
	}
	return nil
}

// refreshCodex trades a Codex refresh token for new tokens. The refresh token
// rotates, so the one returned replaces the old.
func refreshCodex(ctx context.Context, refresh string) (string, string, string, error) {
	body, _ := json.Marshal(map[string]string{"client_id": codexClientID, "grant_type": "refresh_token",
		"refresh_token": refresh, "scope": "openid profile email"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", errors.New("Codex token refresh: " + err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var fresh struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if res.StatusCode != 200 || json.Unmarshal(b, &fresh) != nil || fresh.AccessToken == "" {
		return "", "", "", errors.New("Codex is signed out (token refresh failed); run codex login")
	}
	return fresh.IDToken, fresh.AccessToken, fresh.RefreshToken, nil
}

// setClaudeOAuth writes a's token fields onto oauth, leaving every other field
// of the block alone.
func setClaudeOAuth(oauth map[string]any, a claudeAuth) map[string]any {
	if oauth == nil {
		oauth = map[string]any{}
	}
	oauth["accessToken"] = a.AccessToken
	oauth["refreshToken"] = a.RefreshToken
	oauth["expiresAt"] = a.ExpiresAt
	if a.RefreshExpiresAt != 0 {
		oauth["refreshTokenExpiresAt"] = a.RefreshExpiresAt
	}
	if len(a.Scopes) > 0 {
		oauth["scopes"] = a.Scopes
	}
	if a.SubscriptionType != "" {
		oauth["subscriptionType"] = a.SubscriptionType
	}
	if a.RateLimitTier != "" {
		oauth["rateLimitTier"] = a.RateLimitTier
	}
	return oauth
}

// applyClaudeHeaders makes a request look like Claude Code's, signed with tok.
func applyClaudeHeaders(req *http.Request, tok string, body []byte) {
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

// applyCodexHeaders signs a request to the ChatGPT backend with tok.
func applyCodexHeaders(req *http.Request, tok, accountID string) {
	req.Header.Set("Authorization", "Bearer "+tok)
	if accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "magpie")
}

// claudeCredentialPath is Claude Code's credential file (when not in the Keychain).
func claudeCredentialPath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}
