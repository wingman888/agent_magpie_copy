package provider

// Remembered subscription accounts. Claude Code and Codex keep one sign-in
// each, so magpie remembers every account the user has signed in to and can
// switch between them, or keep several "on" at once so the gateway can fall
// back from one to the next.
//
// Layout, all in magpie's config directory and readable by the user only:
//
//	accounts.json          the index: which accounts, their plan, and "on"
//	accounts/<file>.json   each account's credentials, as the agent keeps them
//
// Refresh tokens have one holder at a time. The account the agent is signed
// in to is refreshed only by the agent's own file, so the remembered copy of
// that account keeps its access token but not its refresh token (see
// withoutRefresh). When a switch replaces it, its whole sign-in, refresh token
// included, is saved back into its remembered file, so no latest token is
// lost. The account a switch brings in has its refresh token removed from its
// remembered file once it is written into the agent's file. Every other
// account keeps its full sign-in and is refreshed from its remembered file,
// and only there. Nothing marked "on" is ever written into the agent's own
// store.
//
// A name that stands in for a missing email (see standIn) is not a key: two
// different accounts that show the same stand-in are never merged. Such a name
// is only reused when the stored sign-in has the same refresh token, or none
// (the copy kept while the account was signed in).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// RememberedAccount is one account magpie knows for an agent.
type RememberedAccount struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	Live  bool   `json:"live"` // the agent is signed in to it now
	On    bool   `json:"on"`   // used alongside the signed-in account (always true when Live)
}

type rememberedAccount struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	Plan  string `json:"plan,omitempty"`
	On    bool   `json:"on,omitempty"`
	File  string `json:"file"`
	// Fp is a digest of the refresh token last seen for this account. It is
	// not usable as a token; it tells apart stand-in names whose copy kept no
	// refresh token.
	Fp string `json:"fp,omitempty"`
}

type accountIndex struct {
	Accounts []rememberedAccount `json:"accounts"`
}

// accountsMu serializes every read and write of the index and credential files.
var accountsMu sync.Mutex

func configDir() string { return filepath.Dir(Path()) }

func indexPath() string { return filepath.Join(configDir(), "accounts.json") }

func credDir() string { return filepath.Join(configDir(), "accounts") }

func (a rememberedAccount) path() string { return filepath.Join(credDir(), a.File) }

// credFile names an account's credential file: the agent, and a digest of the
// account (case-insensitive), so no email appears in a file name.
func credFile(agent, user string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + strings.ToLower(user)))
	return agent + "-" + hex.EncodeToString(sum[:8]) + ".json"
}

func loadIndex() accountIndex {
	var ix accountIndex
	if b, err := os.ReadFile(indexPath()); err == nil {
		json.Unmarshal(b, &ix)
	}
	return ix
}

func saveIndex(ix accountIndex) error {
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return writeSecret(indexPath(), append(b, '\n'))
}

// writeSecret writes a file only the user can read.
func writeSecret(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (ix accountIndex) find(agent, user string) int {
	for i, a := range ix.Accounts {
		if a.Agent == agent && strings.EqualFold(a.User, user) {
			return i
		}
	}
	return -1
}

func (ix accountIndex) count(agent string) int {
	n := 0
	for _, a := range ix.Accounts {
		if a.Agent == agent {
			n++
		}
	}
	return n
}

// checkAgent accepts the agents whose subscriptions can be remembered.
func checkAgent(agent string) error {
	switch agent {
	case "claude", "codex":
		return nil
	}
	return fmt.Errorf("accounts are kept for claude and codex, not %q", agent)
}

func agentName(agent string) string {
	if agent == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// rememberLive records the account an agent is signed in to, with the
// credentials it holds now. The first account remembered is not on; every
// one after it is, so a second account works alongside the first.
func rememberLive(agent, user, plan string, blob []byte) {
	if len(blob) == 0 {
		return
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	ix := loadIndex()
	i, ok := ownEntry(&ix, agent, user, blob)
	if !ok {
		return // another account with the same stand-in name; never overwrite it
	}
	changed := false
	if i < 0 {
		ix.Accounts = append(ix.Accounts, rememberedAccount{Agent: agent, User: user, Plan: plan,
			On: ix.count(agent) > 0, File: credFile(agent, user)})
		i = len(ix.Accounts) - 1
		changed = true
	} else if ix.Accounts[i].Plan != plan {
		ix.Accounts[i].Plan = plan
		changed = true
	}
	if fp := fingerprint(refreshOf(agent, blob)); ix.Accounts[i].Fp != fp {
		ix.Accounts[i].Fp = fp
		changed = true
	}
	// the agent's file keeps the refresh token; the copy here does not
	kept := withoutRefresh(agent, blob)
	path := ix.Accounts[i].path()
	if old, err := os.ReadFile(path); err != nil || !bytes.Equal(old, kept) {
		if writeSecret(path, kept) != nil {
			return
		}
	}
	if changed {
		saveIndex(ix)
		invalidateLoginUsage()
	}
}

// standIn reports whether a name is the placeholder magpie shows when the
// sign-in carries no email and no account id.
func standIn(user string) bool {
	return !strings.Contains(user, "@") && !strings.HasPrefix(user, "ChatGPT ")
}

// codexUser names a ChatGPT sign-in: its email, else its account id, which is
// stable, else a stand-in.
func codexUser(id map[string]any, accountID string) string {
	if u := claimString(id, "email"); u != "" {
		return u
	}
	if accountID != "" {
		return "ChatGPT " + accountID
	}
	return "ChatGPT"
}

// refreshOf is the refresh token in a sign-in blob, or "".
func refreshOf(agent string, blob []byte) string {
	var m map[string]any
	if json.Unmarshal(blob, &m) != nil {
		return ""
	}
	switch agent {
	case "claude":
		o, _ := m["claudeAiOauth"].(map[string]any)
		s, _ := o["refreshToken"].(string)
		return s
	case "codex":
		t, _ := m["tokens"].(map[string]any)
		s, _ := t["refresh_token"].(string)
		return s
	}
	return ""
}

// withoutRefresh is a sign-in with its refresh token removed. It is what
// magpie keeps for the account the agent is signed in to, whose refresh token
// belongs to the agent's own file. The access token stays, so the gateway can
// still use the copy while it is fresh.
func withoutRefresh(agent string, blob []byte) []byte {
	var m map[string]any
	if json.Unmarshal(blob, &m) != nil || m == nil {
		return blob
	}
	switch agent {
	case "claude":
		o, _ := m["claudeAiOauth"].(map[string]any)
		if o == nil {
			return blob
		}
		delete(o, "refreshToken")
		delete(o, "refreshTokenExpiresAt")
	case "codex":
		t, _ := m["tokens"].(map[string]any)
		if t == nil {
			return blob
		}
		delete(t, "refresh_token")
	default:
		return blob
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return blob
	}
	return append(out, '\n')
}

// fingerprint digests a refresh token for Fp; "" for none.
func fingerprint(rt string) string {
	if rt == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(rt))
	return hex.EncodeToString(sum[:])
}

// ownEntry finds the index entry a sign-in belongs to. ok is false when the
// sign-in may be a different account that shares a stand-in name with the
// entry; then nothing may be written there. i is -1 when the account is new.
// The caller holds accountsMu.
//
// A stand-in name is the same account when the entry's file holds the same
// refresh token. When the file has none (the account was signed in, so its
// copy was stripped), the fingerprint last seen must match. Anything else is
// treated as a different account, so nothing is merged or overwritten.
func ownEntry(ix *accountIndex, agent, user string, blob []byte) (i int, ok bool) {
	i = ix.find(agent, user)
	if i < 0 || !standIn(user) {
		return i, true
	}
	rt := refreshOf(agent, blob)
	if stored, err := os.ReadFile(ix.Accounts[i].path()); err == nil {
		if srt := refreshOf(agent, stored); srt != "" {
			return i, srt == rt
		}
	}
	fp := ix.Accounts[i].Fp
	return i, fp == "" || fp == fingerprint(rt)
}

// ambiguousAccount is the error when the signed-in account shows a stand-in
// name that magpie already holds for a different account.
func ambiguousAccount(agent, user string) error {
	return fmt.Errorf("the %s account %q may be another account already remembered under that name; sign in again so it has an email, then retry", agentName(agent), user)
}

// rememberFull saves a sign-in whole, refresh token included, as the entry of
// an account that is not the one the agent uses now. It returns the entry's
// index. The caller holds accountsMu.
func rememberFull(ix *accountIndex, agent string, cur login) (int, error) {
	j, ok := ownEntry(ix, agent, cur.user, cur.blob)
	if !ok {
		return -1, ambiguousAccount(agent, cur.user)
	}
	if j < 0 {
		ix.Accounts = append(ix.Accounts, rememberedAccount{Agent: agent, User: cur.user,
			On: ix.count(agent) > 0, File: credFile(agent, cur.user)})
		j = len(ix.Accounts) - 1
	}
	ix.Accounts[j].Plan = cur.plan
	ix.Accounts[j].Fp = fingerprint(refreshOf(agent, cur.blob))
	if err := writeSecret(ix.Accounts[j].path(), cur.blob); err != nil {
		return -1, err
	}
	return j, nil
}

// withAlternates adds the remembered accounts of the same agent that are on,
// in the order they were remembered, behind the account signed in now.
func withAlternates(live Provider) Provider {
	if live.Account == nil {
		return live
	}
	ix := loadIndex()
	var alts []Provider
	for _, a := range ix.Accounts {
		if a.Agent != live.ID || !a.On || strings.EqualFold(a.User, live.Account.User) {
			continue
		}
		if p, ok := rememberedProvider(a); ok {
			alts = append(alts, p)
		}
	}
	live.Account.alts = alts
	return live
}

// rememberedProvider is a remembered account as a provider, signed with its
// own credentials file. False when that file is missing or unusable.
func rememberedProvider(a rememberedAccount) (Provider, bool) {
	path := a.path()
	switch a.Agent {
	case "claude":
		b, err := os.ReadFile(path)
		if err != nil {
			return Provider{}, false
		}
		if _, ok := parseClaudeCredentials(b); !ok {
			return Provider{}, false
		}
		return claudeAccountOf(a.User, a.Plan, func(ctx context.Context) (string, error) {
			return claudeTokenFile(ctx, path)
		}), true
	case "codex":
		var x codexAuth
		if !readJSON(path, &x) || x.Tokens.AccessToken == "" {
			return Provider{}, false
		}
		return codexAccountOf(a.User, a.Plan, path), true
	}
	return Provider{}, false
}

// claudeTokenFile is claudeToken for a remembered account: the token comes
// from, and any refresh is written back to, that account's own file.
func claudeTokenFile(ctx context.Context, path string) (string, error) {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("Claude Code account is not remembered any more; magpie accounts add claude")
	}
	c, ok := parseClaudeCredentials(b)
	if !ok {
		return "", errors.New("Claude Code account has no usable sign-in; sign in to it again")
	}
	if claudeFresh(c) {
		return c.OAuth.AccessToken, nil
	}
	c, err = claudeRefresh(ctx, c)
	if err != nil {
		return "", err
	}
	out, err := c.marshal()
	if err != nil {
		return "", err
	}
	if err := writeSecret(path, append(out, '\n')); err != nil {
		return "", err
	}
	return c.OAuth.AccessToken, nil
}

// marshalOrNil is the credential blob as magpie stores it, or nil.
func (c claudeCredentials) marshalOrNil() []byte {
	b, err := c.marshal()
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

// login is the sign-in an agent holds now, read from its own files.
type login struct {
	user, plan string
	blob       []byte               // the whole sign-in, as magpie remembers it
	claude     claudeCredentials    // claude: the credential file's contents
	loc        claudeCredentialLocation
}

func claudeFilePath() string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	return filepath.Join(dir, ".credentials.json")
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "auth.json")
}

// agentLogin reads the sign-in an agent is using right now.
func agentLogin(agent string) (login, bool) {
	switch agent {
	case "claude":
		forgetClaudeStatus()
		c, loc, ok := readClaudeCredential()
		if !ok {
			return login{}, false
		}
		user, plan, signedOut := claudeWho(c)
		if signedOut {
			return login{}, false
		}
		return login{user: user, plan: plan, blob: c.marshalOrNil(), claude: c, loc: loc}, true
	case "codex":
		path := codexAuthPath()
		var a codexAuth
		if !readJSON(path, &a) || a.Tokens.AccessToken == "" || a.AuthMode == "apikey" {
			return login{}, false
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return login{}, false
		}
		id := jwtClaims(a.Tokens.IDToken)
		user, plan := codexUser(id, codexAccountID(a)), claimString(id, "https://api.openai.com/auth", "chatgpt_plan_type")
		return login{user: user, plan: plan, blob: raw}, true
	}
	return login{}, false
}

// writeLogin makes target the agent's sign-in. Only the login fields of the
// agent's file change; every other field is kept as it is. live is the
// sign-in being replaced, or nil when the agent is signed out.
func writeLogin(agent string, live *login, target []byte) error {
	switch agent {
	case "claude":
		tc, ok := parseClaudeCredentials(target)
		if !ok {
			return errors.New("the remembered Claude Code account has no usable sign-in")
		}
		base, loc := claudeCredentials{}, claudeCredentialLocation{path: claudeFilePath()}
		if live != nil {
			base, loc = live.claude, live.loc
		}
		raw := base.raw
		if raw == nil {
			raw = map[string]any{}
		}
		// replace only the login fields inside claudeAiOauth; anything else
		// the agent keeps there stays
		oauth, _ := raw["claudeAiOauth"].(map[string]any)
		if oauth == nil {
			oauth = map[string]any{}
		}
		src, _ := tc.raw["claudeAiOauth"].(map[string]any)
		for _, k := range claudeLoginKeys {
			if v, ok := src[k]; ok {
				oauth[k] = v
			} else {
				delete(oauth, k)
			}
		}
		raw["claudeAiOauth"] = oauth
		return saveClaudeCredential(loc, claudeCredentials{raw: raw, OAuth: tc.OAuth})
	case "codex":
		var tgt map[string]any
		if json.Unmarshal(target, &tgt) != nil {
			return errors.New("the remembered Codex account has no usable sign-in")
		}
		raw := map[string]any{}
		path := codexAuthPath()
		if b, err := os.ReadFile(path); err == nil {
			if json.Unmarshal(b, &raw) != nil || raw == nil {
				return fmt.Errorf("%s is not valid JSON; not overwriting it", path)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		for _, k := range []string{"auth_mode", "tokens", "last_refresh"} {
			if v, ok := tgt[k]; ok {
				raw[k] = v
			} else {
				delete(raw, k)
			}
		}
		out, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		return writeSecret(path, append(out, '\n'))
	}
	return fmt.Errorf("accounts are kept for claude and codex, not %q", agent)
}

// SwitchAccount makes a remembered account the one the agent is signed in to.
// The account it replaces is kept with its latest sign-in, and takes over the
// "on" state of the account it was replaced by. Sessions already running keep
// the old account until they restart.
func SwitchAccount(agent, user string) error {
	if err := checkAgent(agent); err != nil {
		return err
	}
	Accounts() // remembers the sign-in in use first

	// The agent's refresh token is rotated by the agent and by claudeToken /
	// codexToken, under these locks. Hold the same lock for the whole switch so
	// no refresh writes the agent's file between reading the sign-in in use
	// and replacing it. Accounts() above takes no lock held here, and nothing
	// below takes these locks again.
	mu := &claudeMu
	if agent == "codex" {
		mu = &codexMu
	}
	mu.Lock()
	defer mu.Unlock()
	accountsMu.Lock()
	defer accountsMu.Unlock()
	defer invalidateLoginUsage()

	ix := loadIndex()
	t := ix.find(agent, user)
	if t < 0 {
		return fmt.Errorf("no remembered %s account %q; magpie accounts %s lists them", agentName(agent), user, agent)
	}
	target := ix.Accounts[t]
	tb, err := os.ReadFile(target.path())
	if err != nil {
		return fmt.Errorf("%s account %s has no saved sign-in: %v", agentName(agent), target.User, err)
	}
	if refreshOf(agent, tb) == "" {
		// its copy was kept while it was signed in, and the agent was then
		// signed in to another account outside magpie, taking the refresh token
		return fmt.Errorf("the saved %s sign-in for %s has no refresh token; sign in to it again with magpie accounts add %s",
			agentName(agent), target.User, agent)
	}
	cur, has := agentLogin(agent)
	if has {
		j, ok := ownEntry(&ix, agent, cur.user, cur.blob)
		if !ok {
			return ambiguousAccount(agent, cur.user)
		}
		if j == t {
			return nil
		}
	}
	var live *login
	if has {
		// the account replaced keeps its latest sign-in, refresh token included
		j, err := rememberFull(&ix, agent, cur)
		if err != nil {
			return err
		}
		ix.Accounts[j].On = target.On
		live = &cur
	}
	if err := writeLogin(agent, live, tb); err != nil {
		return err
	}
	// the target's refresh token now belongs to the agent's file alone
	ix.Accounts[t].Fp = fingerprint(refreshOf(agent, tb))
	if err := writeSecret(target.path(), withoutRefresh(agent, tb)); err != nil {
		return err
	}
	return saveIndex(ix)
}

// claudeLoginKeys are the fields of claudeAiOauth that a sign-in owns.
var claudeLoginKeys = []string{"accessToken", "refreshToken", "expiresAt", "refreshTokenExpiresAt",
	"scopes", "subscriptionType", "rateLimitTier"}

// codexAccountID is the account id of a Codex sign-in, from the file or the
// id token.
func codexAccountID(a codexAuth) string {
	if a.Tokens.AccountID != "" {
		return a.Tokens.AccountID
	}
	return claimString(jwtClaims(a.Tokens.IDToken), "https://api.openai.com/auth", "chatgpt_account_id")
}

// keepSignIn saves the sign-in in use, whole, before an agent's own login
// replaces it, so the account it leaves stays usable. The caller must not
// hold accountsMu.
func keepSignIn(agent string) {
	cur, has := agentLogin(agent)
	if !has {
		return
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	ix := loadIndex()
	if _, err := rememberFull(&ix, agent, cur); err != nil {
		return
	}
	saveIndex(ix)
}

// ForgetAccount drops a remembered account. The one signed in to cannot be
// forgotten; sign it out, or switch away from it, first.
func ForgetAccount(agent, user string) error {
	if err := checkAgent(agent); err != nil {
		return err
	}
	Accounts()
	accountsMu.Lock()
	defer accountsMu.Unlock()
	cur, has := agentLogin(agent)
	if has && strings.EqualFold(cur.user, user) {
		return fmt.Errorf("%s is the %s account in use; switch to another account before forgetting it", user, agentName(agent))
	}
	ix := loadIndex()
	i := ix.find(agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", agentName(agent), user)
	}
	os.Remove(ix.Accounts[i].path())
	ix.Accounts = append(ix.Accounts[:i], ix.Accounts[i+1:]...)
	invalidateLoginUsage()
	return saveIndex(ix)
}

// SetLoginOn makes a remembered account used alongside the signed-in one, or
// stops that. The account signed in to is always on.
func SetLoginOn(agent, user string, on bool) error {
	if err := checkAgent(agent); err != nil {
		return err
	}
	Accounts()
	accountsMu.Lock()
	defer accountsMu.Unlock()
	cur, has := agentLogin(agent)
	ix := loadIndex()
	i := ix.find(agent, user)
	if i < 0 {
		return fmt.Errorf("no remembered %s account %q", agentName(agent), user)
	}
	if has && strings.EqualFold(cur.user, user) {
		if !on {
			return fmt.Errorf("%s is the %s account in use, so it stays on", ix.Accounts[i].User, agentName(agent))
		}
		return nil
	}
	ix.Accounts[i].On = on
	return saveIndex(ix)
}

// Remembered lists the accounts magpie knows for an agent: the one signed in
// to first, then the rest in the order they were remembered.
func Remembered(agent string) []RememberedAccount {
	if checkAgent(agent) != nil {
		return nil
	}
	Accounts()
	cur, has := agentLogin(agent)
	accountsMu.Lock()
	defer accountsMu.Unlock()
	ix := loadIndex()
	var out []RememberedAccount
	if has {
		if i := ix.find(agent, cur.user); i >= 0 {
			out = append(out, RememberedAccount{Agent: agent, User: ix.Accounts[i].User, Plan: cur.plan, Live: true, On: true})
		}
	}
	for _, a := range ix.Accounts {
		if a.Agent != agent || (has && strings.EqualFold(a.User, cur.user)) {
			continue
		}
		out = append(out, RememberedAccount{Agent: agent, User: a.User, Plan: a.Plan, On: a.On})
	}
	return out
}

// Login runs the agent's own sign-in, which opens the vendor's login page, and
// remembers the account it leaves signed in. The account signed in before is
// kept, so nothing is lost.
func Login(agent string) error {
	if err := checkAgent(agent); err != nil {
		return err
	}
	keepSignIn(agent)
	var cmd *exec.Cmd
	if agent == "claude" {
		exe := claudeExecutable()
		if exe == "" {
			return errors.New("claude is not installed; install Claude Code first")
		}
		cmd = exec.Command(exe, "auth", "login")
	} else {
		exe, err := exec.LookPath("codex")
		if err != nil {
			return errors.New("codex is not installed; install the Codex CLI first")
		}
		cmd = exec.Command(exe, "login")
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s sign-in did not finish: %v", agentName(agent), err)
	}
	forgetClaudeStatus()
	forgetClaudeCredential()
	if _, ok := agentLogin(agent); !ok {
		return fmt.Errorf("%s is still signed out", agentName(agent))
	}
	Accounts()
	invalidateLoginUsage()
	return nil
}
