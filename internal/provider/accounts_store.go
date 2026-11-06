package provider

// Subscription accounts: the Claude Code and Codex sign-ins magpie remembers.
//
// The agent's own store holds the sign-in the agent uses now (Claude Code's
// .credentials.json or Keychain item, Codex's auth.json). Every other account
// magpie remembers keeps its sign-in in a folder of its own under the magpie
// config directory: the folder is 0700 and its files 0600. A refresh token has
// one holder at a time: the agent's store for the signed-in account, the folder
// for the rest. An account that is on alongside the signed-in one is used by the
// gateway from its folder, and its sign-in is never written into the agent's
// store.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
)

// storeMu guards the index of remembered accounts.
var storeMu sync.Mutex

// codexLoginKeys are the fields of Codex's auth.json that hold the sign-in. The
// rest of the file is Codex's own and is left alone.
var codexLoginKeys = []string{"auth_mode", "tokens", "last_refresh", "OPENAI_API_KEY"}

// storedAccount is one remembered account in the index.
type storedAccount struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	On    bool   `json:"on,omitempty"` // used by the gateway alongside the signed-in account
}

type accountIndex struct {
	Accounts []storedAccount `json:"accounts"`
}

// RememberedAccount describes one account magpie knows, for listing.
type RememberedAccount struct {
	Agent   string
	User    string
	Plan    string
	Current bool // the agent is signed in to it now
	On      bool // used by the gateway alongside the signed-in account; always true for the current one
}

// AccountAgent maps an agent name, or an alias, to the agent whose subscription
// accounts magpie keeps. ok is false for any other agent.
func AccountAgent(name string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "claude", "cc", "claude-code":
		return "claude", true
	case "codex":
		return "codex", true
	}
	return "", false
}

func unknownAccountAgent(name string) error {
	return fmt.Errorf("unknown agent %q: subscription accounts are kept for claude (cc) and codex", name)
}

func noSuchAccount(agent, user string) error {
	return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agent, user, agent)
}

func accountRoot() string { return filepath.Join(filepath.Dir(Path()), "accounts") }

// accountFolder is where an account's sign-in waits while another account is
// signed in to the agent.
func accountFolder(agent, user string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + strings.ToLower(strings.TrimSpace(user))))
	return filepath.Join(accountRoot(), agent+"-"+hex.EncodeToString(sum[:8]))
}

// credentialFile names the file in an account's folder that holds its sign-in:
// the name the agent gives its own, so the folder can serve as its config dir.
func credentialFile(agent string) string {
	if agent == "claude" {
		return ".credentials.json"
	}
	return "auth.json"
}

func loadIndex() accountIndex {
	var ix accountIndex
	if b, err := os.ReadFile(filepath.Join(accountRoot(), "index.json")); err == nil {
		json.Unmarshal(b, &ix)
	}
	return ix
}

func saveIndex(ix accountIndex) error {
	return writeJSONSecret(filepath.Join(accountRoot(), "index.json"), ix)
}

// find returns the position of an account in the index, or -1.
func (ix *accountIndex) find(agent, user string) int {
	for i, a := range ix.Accounts {
		if a.Agent == agent && strings.EqualFold(a.User, user) {
			return i
		}
	}
	return -1
}

// writeSecret writes a file only its owner can read, creating its folder
// private as well.
func writeSecret(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func writeJSONSecret(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(path, append(b, '\n'))
}

// jsonField reads one top-level field of a JSON file.
func jsonField(path, key string) (any, bool) {
	var m map[string]any
	if !readJSON(path, &m) {
		return nil, false
	}
	v, ok := m[key]
	return v, ok && v != nil
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// claudeConfigDir is where Claude Code keeps its credentials.
func claudeConfigDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// claudeStateFile is Claude Code's global config. It names the account signed
// in (oauthAccount) and holds much else that must survive a switch.
func claudeStateFile() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// claudeStoreLocation is where Claude Code's sign-in goes when none is stored yet.
func claudeStoreLocation() claudeCredentialLocation {
	if claudeKeychain {
		return claudeCredentialLocation{keychain: true, account: os.Getenv("USER")}
	}
	return claudeCredentialLocation{path: filepath.Join(claudeConfigDir(), ".credentials.json")}
}

func codexAuthFile() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// currentLogin is the account the agent is signed in to now, if any. It does
// not touch the index.
func currentLogin(agent string) (Provider, bool) {
	switch agent {
	case "claude":
		return claudeAccount()
	case "codex":
		home, _ := os.UserHomeDir()
		return codexAccount(home)
	}
	return Provider{}, false
}

// rememberLogins records the sign-ins the agents hold now. A copy of one of
// them kept in its folder is dropped: the agent's store is the newer holder.
func rememberLogins() {
	storeMu.Lock()
	defer storeMu.Unlock()
	ix := loadIndex()
	changed := false
	for _, agent := range []string{"claude", "codex"} {
		p, ok := currentLogin(agent)
		if !ok || p.Account == nil || p.Account.User == "" {
			continue
		}
		user := p.Account.User
		i := ix.find(agent, user)
		if i < 0 {
			ix.Accounts = append(ix.Accounts, storedAccount{Agent: agent, User: user, Plan: p.Account.Plan})
			changed = true
		} else if ix.Accounts[i].Plan != p.Account.Plan {
			ix.Accounts[i].Plan = p.Account.Plan
			changed = true
		}
		os.RemoveAll(accountFolder(agent, user))
	}
	if changed {
		saveIndex(ix)
	}
}

// captureLogin copies the sign-in the agent holds into user's folder. The
// agent's store keeps its copy; the caller decides what happens to it.
func captureLogin(agent, user string) error {
	dir := accountFolder(agent, user)
	switch agent {
	case "claude":
		c, _, ok := readClaudeCredential()
		if !ok {
			return errors.New("Claude Code is not signed in")
		}
		if err := writeJSONSecret(filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": c.raw["claudeAiOauth"]}); err != nil {
			return err
		}
		if oa, ok := jsonField(claudeStateFile(), "oauthAccount"); ok {
			return writeJSONSecret(filepath.Join(dir, ".claude.json"), map[string]any{"oauthAccount": oa})
		}
		return os.RemoveAll(filepath.Join(dir, ".claude.json"))
	case "codex":
		var raw map[string]any
		if !readJSON(codexAuthFile(), &raw) || len(raw) == 0 {
			return errors.New("Codex is not signed in")
		}
		part := map[string]any{}
		for _, k := range codexLoginKeys {
			if v, ok := raw[k]; ok {
				part[k] = v
			}
		}
		return writeJSONSecret(filepath.Join(dir, "auth.json"), part)
	}
	return unknownAccountAgent(agent)
}

// restoreLogin moves user's sign-in from its folder into the agent's store,
// changing only the login fields, and drops the folder's copy.
func restoreLogin(agent, user string) error {
	dir := accountFolder(agent, user)
	switch agent {
	case "claude":
		b, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
		part, ok := parseClaudeCredentials(b)
		if err != nil || !ok {
			return fmt.Errorf("no saved Claude Code sign-in for %s", user)
		}
		cur, loc, ok := readClaudeCredential()
		if !ok {
			loc = claudeStoreLocation()
		}
		raw := copyMap(cur.raw)
		raw["claudeAiOauth"] = part.raw["claudeAiOauth"]
		if !loc.keychain {
			if err := os.MkdirAll(filepath.Dir(loc.path), 0o700); err != nil {
				return err
			}
		}
		if err := saveClaudeCredential(loc, claudeCredentials{raw: raw, OAuth: part.OAuth}); err != nil {
			return err
		}
		state := claudeStateFile()
		if oa, ok := jsonField(filepath.Join(dir, ".claude.json"), "oauthAccount"); ok {
			if err := edit.SetJSON(state, edit.KV{Path: "oauthAccount", Value: oa}); err != nil {
				return err
			}
		} else if _, err := os.Stat(state); err == nil {
			if err := edit.DelJSON(state, "oauthAccount"); err != nil {
				return err
			}
		}
		forgetClaudeStatus()
	case "codex":
		var part map[string]any
		if !readJSON(filepath.Join(dir, "auth.json"), &part) || len(part) == 0 {
			return fmt.Errorf("no saved Codex sign-in for %s", user)
		}
		path := codexAuthFile()
		cur := map[string]any{}
		readJSON(path, &cur)
		for _, k := range codexLoginKeys {
			delete(cur, k)
			if v, ok := part[k]; ok {
				cur[k] = v
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := writeJSONSecret(path, cur); err != nil {
			return err
		}
	default:
		return unknownAccountAgent(agent)
	}
	return os.RemoveAll(dir)
}

// RememberedAccounts lists the agent's accounts magpie knows, in the order they
// were saved. The one the agent is signed in to is marked Current.
func RememberedAccounts(agent string) ([]RememberedAccount, error) {
	a, ok := AccountAgent(agent)
	if !ok {
		return nil, unknownAccountAgent(agent)
	}
	rememberLogins()
	storeMu.Lock()
	defer storeMu.Unlock()
	cur, signedIn := currentLogin(a)
	var out []RememberedAccount
	for _, s := range loadIndex().Accounts {
		if s.Agent != a {
			continue
		}
		r := RememberedAccount{Agent: a, User: s.User, Plan: s.Plan, On: s.On}
		if signedIn && strings.EqualFold(cur.Account.User, s.User) {
			r.Current, r.On, r.Plan = true, true, cur.Account.Plan
		}
		out = append(out, r)
	}
	return out, nil
}

// SetLoginOn keeps a remembered account on alongside the one the agent is
// signed in to, or takes it off. The signed-in account is always on, so turning
// it off is refused.
func SetLoginOn(agent, user string, on bool) error {
	a, ok := AccountAgent(agent)
	if !ok {
		return unknownAccountAgent(agent)
	}
	rememberLogins()
	storeMu.Lock()
	defer storeMu.Unlock()
	ix := loadIndex()
	i := ix.find(a, user)
	if i < 0 {
		return noSuchAccount(a, user)
	}
	if cur, signedIn := currentLogin(a); signedIn && strings.EqualFold(cur.Account.User, user) {
		if !on {
			return fmt.Errorf("%s is the %s account signed in now, and it stays on", user, a)
		}
		return nil
	}
	ix.Accounts[i].On = on
	return saveIndex(ix)
}

// ForgetAccount drops a remembered account and its sign-in. The account the
// agent is signed in to cannot be forgotten; sign the agent in to another first.
func ForgetAccount(agent, user string) error {
	a, ok := AccountAgent(agent)
	if !ok {
		return unknownAccountAgent(agent)
	}
	rememberLogins()
	storeMu.Lock()
	defer storeMu.Unlock()
	ix := loadIndex()
	i := ix.find(a, user)
	if i < 0 {
		return noSuchAccount(a, user)
	}
	if cur, signedIn := currentLogin(a); signedIn && strings.EqualFold(cur.Account.User, user) {
		return fmt.Errorf("%s is the %s account signed in now; switch to another account before forgetting it", user, a)
	}
	ix.Accounts = append(ix.Accounts[:i], ix.Accounts[i+1:]...)
	if err := saveIndex(ix); err != nil {
		return err
	}
	return os.RemoveAll(accountFolder(a, user))
}

// SwitchAccount signs the agent in as a remembered account. The account signed
// in until now keeps its latest sign-in and takes over the target's on-state;
// the target's sign-in moves into the agent's store, and the rest of the agent's
// config is left as it was. Sessions already running keep the old account until
// they restart.
func SwitchAccount(agent, user string) (RememberedAccount, error) {
	a, ok := AccountAgent(agent)
	if !ok {
		return RememberedAccount{}, unknownAccountAgent(agent)
	}
	rememberLogins()
	storeMu.Lock()
	defer storeMu.Unlock()
	ix := loadIndex()
	i := ix.find(a, user)
	if i < 0 {
		return RememberedAccount{}, noSuchAccount(a, user)
	}
	target := ix.Accounts[i]
	cur, signedIn := currentLogin(a)
	if signedIn && strings.EqualFold(cur.Account.User, target.User) {
		return RememberedAccount{Agent: a, User: target.User, Plan: cur.Account.Plan, Current: true, On: true}, nil
	}
	if _, err := os.Stat(filepath.Join(accountFolder(a, target.User), credentialFile(a))); err != nil {
		return RememberedAccount{}, fmt.Errorf("no saved sign-in for %s; sign in to it again with magpie accounts add %s", target.User, a)
	}
	if signedIn {
		if err := captureLogin(a, cur.Account.User); err != nil {
			return RememberedAccount{}, err
		}
	}
	if err := restoreLogin(a, target.User); err != nil {
		return RememberedAccount{}, err
	}
	if signedIn {
		j := ix.find(a, cur.Account.User)
		if j < 0 {
			ix.Accounts = append(ix.Accounts, storedAccount{Agent: a, User: cur.Account.User})
			j = len(ix.Accounts) - 1
		}
		ix.Accounts[j].On = target.On
		ix.Accounts[j].Plan = cur.Account.Plan
	}
	ix.Accounts[i].On = false
	if err := saveIndex(ix); err != nil {
		return RememberedAccount{}, err
	}
	return RememberedAccount{Agent: a, User: target.User, Plan: target.Plan, Current: true}, nil
}

// AddAccount signs the agent in to an account with the vendor's own sign-in
// page, then remembers it. The account signed in before stays signed in, and the
// new one is on alongside it. Once a sign-in is kept, it is not read again.
func AddAccount(agent string) (RememberedAccount, error) {
	a, ok := AccountAgent(agent)
	if !ok {
		return RememberedAccount{}, unknownAccountAgent(agent)
	}
	rememberLogins()
	prev, had := currentLogin(a)
	if had {
		if err := captureLogin(a, prev.Account.User); err != nil {
			return RememberedAccount{}, err
		}
	}
	// back signs the previous account in again when the new one cannot be kept
	back := func(err error) (RememberedAccount, error) {
		if had {
			if rerr := restoreLogin(a, prev.Account.User); rerr != nil {
				err = fmt.Errorf("%w (and %s could not be signed in again: %v)", err, prev.Account.User, rerr)
			}
		}
		return RememberedAccount{}, err
	}
	if err := loginCommand(a); err != nil {
		return back(err)
	}
	cur, now := currentLogin(a)
	if !now {
		return back(errors.New("no sign-in found after the login"))
	}
	user := cur.Account.User
	same := had && strings.EqualFold(user, prev.Account.User)
	switch {
	case same:
		// the same account signed in again: its new sign-in stays in the agent
		os.RemoveAll(accountFolder(a, user))
	case had:
		if err := captureLogin(a, user); err != nil {
			return back(err)
		}
	default:
		os.RemoveAll(accountFolder(a, user))
	}

	storeMu.Lock()
	ix := loadIndex()
	i := ix.find(a, user)
	if i < 0 {
		// a second account is on alongside the first by default
		ix.Accounts = append(ix.Accounts, storedAccount{Agent: a, User: user, Plan: cur.Account.Plan, On: had})
		i = len(ix.Accounts) - 1
	} else {
		ix.Accounts[i].Plan = cur.Account.Plan
	}
	err := saveIndex(ix)
	on := ix.Accounts[i].On
	storeMu.Unlock()
	if err != nil {
		return back(err)
	}
	if had && !same {
		if err := restoreLogin(a, prev.Account.User); err != nil {
			return RememberedAccount{}, err
		}
	}
	return RememberedAccount{Agent: a, User: user, Plan: cur.Account.Plan, Current: same, On: on || same}, nil
}

// loginCommand runs the vendor's own sign-in and returns once it is done. The
// sign-in page is opened in the browser when the command prints its address.
// A var so tests never open a browser.
var loginCommand = func(agent string) error {
	var name string
	var args []string
	switch agent {
	case "claude":
		if name = claudeExecutable(); name == "" {
			return errors.New("Claude Code is not installed")
		}
		args = []string{"auth", "login"}
	case "codex":
		p, err := exec.LookPath("codex")
		if err != nil {
			return errors.New("Codex is not installed")
		}
		name, args = p, []string{"login"}
	default:
		return unknownAccountAgent(agent)
	}
	watch := &loginPageWatch{}
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.MultiWriter(os.Stdout, watch)
	cmd.Stderr = io.MultiWriter(os.Stderr, watch)
	return cmd.Run()
}

// loginPageRe finds the sign-in address in the vendor's output once its line is complete.
var loginPageRe = regexp.MustCompile(`(https://[^\s"'<>]+)\s`)

// loginPageWatch reads the output of the vendor's sign-in and opens the page
// once. If the browser cannot be started, the address is printed.
type loginPageWatch struct {
	mu    sync.Mutex
	text  string
	found bool
}

func (w *loginPageWatch) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.found {
		return len(p), nil
	}
	w.text += string(p)
	if m := loginPageRe.FindStringSubmatch(w.text); m != nil {
		w.found = true
		openLoginPage(m[1])
	} else if len(w.text) > 1<<16 {
		w.text = w.text[len(w.text)-(1<<12):]
	}
	return len(p), nil
}

// openLoginPage opens the sign-in page in the system browser, best effort.
func openLoginPage(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "open this page to sign in:", u)
		return
	}
	go cmd.Wait()
}

// claudeSigner signs a request to Anthropic with the access token token returns.
func claudeSigner(token func(context.Context) (string, error)) func(ctx context.Context, req *http.Request, body []byte) error {
	return func(ctx context.Context, req *http.Request, body []byte) error {
		tok, err := token(ctx)
		if err != nil {
			return err
		}
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
		return nil
	}
}

// codexSigner signs a request to ChatGPT with the tokens in the auth file at path.
func codexSigner(path string) func(ctx context.Context, req *http.Request, _ []byte) error {
	return func(ctx context.Context, req *http.Request, _ []byte) error {
		tok, accountID, err := codexToken(ctx, path)
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
}

// storedClaudeToken is the access token of a remembered Claude account. It is
// refreshed in the account's own folder when about to expire.
func storedClaudeToken(ctx context.Context, path string) (string, error) {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("saved Claude Code sign-in is missing; sign in to it again")
	}
	c, ok := parseClaudeCredentials(b)
	if !ok {
		return "", errors.New("saved Claude Code sign-in is unreadable; sign in to it again")
	}
	if claudeFresh(c) {
		return c.OAuth.AccessToken, nil
	}
	fresh, err := refreshClaude(ctx, c)
	if err != nil {
		return "", err
	}
	out, err := fresh.marshal()
	if err != nil {
		return "", err
	}
	if err := writeSecret(path, append(out, '\n')); err != nil {
		return "", err
	}
	return fresh.OAuth.AccessToken, nil
}

// storedAccountOf is the account a remembered folder signs in with; nil when
// the folder holds no sign-in.
func storedAccountOf(agent, user, plan, dir string) *Account {
	path := filepath.Join(dir, credentialFile(agent))
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	acct := &Account{Agent: agent, User: user, Plan: plan, configDir: dir}
	switch agent {
	case "claude":
		acct.sign = claudeSigner(func(ctx context.Context) (string, error) { return storedClaudeToken(ctx, path) })
		acct.body = claudeBody
		acct.models = func() []catalog.Model { return catalog.Provider("anthropic") }
	case "codex":
		acct.Stream = true
		acct.sign = codexSigner(path)
		acct.body = codexBody
		acct.models = catalog.Codex
	default:
		return nil
	}
	return acct
}

// Alongside lists the other accounts of p's agent that are on alongside the one
// p is signed in with, in the order they were saved. Each is p with that
// account's own sign-in, for the gateway to try after p.
func (p Provider) Alongside() []Provider {
	if p.Account == nil {
		return nil
	}
	storeMu.Lock()
	ix := loadIndex()
	storeMu.Unlock()
	var out []Provider
	for _, s := range ix.Accounts {
		if s.Agent != p.Account.Agent || !s.On || strings.EqualFold(s.User, p.Account.User) {
			continue
		}
		acct := storedAccountOf(s.Agent, s.User, s.Plan, accountFolder(s.Agent, s.User))
		if acct == nil {
			continue
		}
		sp := p
		sp.Account = acct
		out = append(out, sp)
	}
	return out
}
