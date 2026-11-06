package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// accountsHome sets up a home with nothing signed in. The claude binary is a
// fake whose `auth status` names the account in the agent's own store: the
// letter in its access token, as <letter>@example.com.
func accountsHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	resetLoginUsage()
	script := filepath.Join(t.TempDir(), "claude")
	body := `#!/bin/sh
tok=$(grep -o 'sk-ant-oat01-[a-z]' "$HOME/.claude/.credentials.json" | head -n 1)
letter=${tok##*-}
echo "{\"loggedIn\":true,\"email\":\"$letter@example.com\",\"subscriptionType\":\"max\"}"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return script }
	forgetClaudeStatus()
	return home
}

// signInClaude writes the sign-in Claude Code keeps for account who ("a", "b",
// …). token varies the access token, as a refresh would.
func signInClaude(t *testing.T, home, who, token string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-" + who + token, "refreshToken": "sk-ant-ort01-" + who,
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max",
		},
		"mcpOAuth": map[string]any{"slack": "keep"},
	})
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{
		"numStartups": 3, "oauthAccount": map[string]any{"emailAddress": who + "@example.com"},
	})
	forgetClaudeCredential()
	forgetClaudeStatus()
}

// codexSignIn writes the auth file Codex keeps for the account with that email.
func codexSignIn(t *testing.T, home, email, account string) {
	t.Helper()
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt", "custom": "keep",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": email}),
			"access_token":  fakeJWT(map[string]any{"exp": exp, "sub": account}),
			"refresh_token": "r-" + account, "account_id": account,
		},
	})
}

// fakeLogin stands in for the vendor's sign-in for the rest of the test.
func fakeLogin(t *testing.T, fn func(agent string) error) {
	t.Helper()
	old := loginCommand
	loginCommand = fn
	t.Cleanup(func() { loginCommand = old })
}

// remembered returns the users of an agent's remembered accounts, in order.
func remembered(t *testing.T, agent string) []RememberedAccount {
	t.Helper()
	rows, err := RememberedAccounts(agent)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// twoClaudeAccounts leaves a@ signed in and b@ remembered on alongside it.
func twoClaudeAccounts(t *testing.T, home string) {
	t.Helper()
	signInClaude(t, home, "a", "1")
	fakeLogin(t, func(agent string) error {
		signInClaude(t, home, "b", "1")
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
}

func TestAddRemembersSecondAccountOn(t *testing.T) {
	home := accountsHome(t)
	signInClaude(t, home, "a", "1")
	fakeLogin(t, func(agent string) error {
		if agent != "claude" {
			t.Fatalf("login for %s", agent)
		}
		signInClaude(t, home, "b", "1")
		return nil
	})

	acct, err := AddAccount("claude")
	if err != nil {
		t.Fatal(err)
	}
	if acct.User != "b@example.com" || acct.Current || !acct.On {
		t.Fatalf("added: %+v", acct)
	}
	rows := remembered(t, "claude")
	if len(rows) != 2 || rows[0].User != "a@example.com" || !rows[0].Current ||
		rows[1].User != "b@example.com" || rows[1].Current || !rows[1].On {
		t.Fatalf("rows: %+v", rows)
	}

	// the agent still signs in as a; b's sign-in waits in its own folder
	agentStore, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if !strings.Contains(string(agentStore), "sk-ant-oat01-a1") || strings.Contains(string(agentStore), "sk-ant-oat01-b1") {
		t.Fatalf("agent store: %s", agentStore)
	}
	folder := accountFolder("claude", "b@example.com")
	if fi, err := os.Stat(filepath.Join(folder, ".credentials.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("folder file: %v %v", fi, err)
	}
	if fi, err := os.Stat(folder); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(accountRoot(), "index.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("index: %v %v", fi, err)
	}
}

func TestSwitchKeepsLatestSignInAndOtherFields(t *testing.T) {
	home := accountsHome(t)
	twoClaudeAccounts(t, home)

	// a refreshes its tokens in the agent's store, and the agent's config has
	// more in it than login
	signInClaude(t, home, "a", "2")
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{
		"numStartups": 3, "projects": map[string]any{"/work": map[string]any{}},
		"oauthAccount": map[string]any{"emailAddress": "a@example.com"},
	})

	got, err := SwitchAccount("claude", "B@Example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "b@example.com" || !got.Current {
		t.Fatalf("switched to %+v", got)
	}

	// the agent now signs in as b, with its MCP state untouched
	var store map[string]any
	readJSON(filepath.Join(home, ".claude", ".credentials.json"), &store)
	oauth, _ := store["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "sk-ant-oat01-b1" || store["mcpOAuth"] == nil {
		t.Fatalf("agent store: %v", store)
	}

	// a's latest sign-in is kept for later
	var kept map[string]any
	readJSON(filepath.Join(accountFolder("claude", "a@example.com"), ".credentials.json"), &kept)
	keptOAuth, _ := kept["claudeAiOauth"].(map[string]any)
	if keptOAuth["accessToken"] != "sk-ant-oat01-a2" {
		t.Fatalf("a's kept sign-in: %v", kept)
	}
	if _, err := os.Stat(accountFolder("claude", "b@example.com")); !os.IsNotExist(err) {
		t.Fatalf("b's folder should be gone: %v", err)
	}

	// the global config names b, and everything else in it is as it was
	var state map[string]any
	readJSON(filepath.Join(home, ".claude.json"), &state)
	if oa, _ := state["oauthAccount"].(map[string]any); oa["emailAddress"] != "b@example.com" {
		t.Fatalf("oauthAccount: %v", state["oauthAccount"])
	}
	if state["numStartups"] != float64(3) || state["projects"] == nil {
		t.Fatalf("other config changed: %v", state)
	}

	// identity follows, and the on-state moved to the account replaced
	if p, ok := find(All(), "claude"); !ok || p.Account == nil || p.Account.User != "b@example.com" {
		t.Fatalf("claude after switch: %+v %v", p, ok)
	}
	rows := remembered(t, "claude")
	if len(rows) != 2 || rows[0].User != "a@example.com" || !rows[0].On || rows[0].Current ||
		rows[1].User != "b@example.com" || !rows[1].Current {
		t.Fatalf("rows after switch: %+v", rows)
	}
}

func TestForgetRefusesTheSignedInAccount(t *testing.T) {
	home := accountsHome(t)
	twoClaudeAccounts(t, home)

	if err := ForgetAccount("claude", "a@example.com"); err == nil {
		t.Fatal("forgot the signed-in account")
	}
	if err := ForgetAccount("claude", "nobody@example.com"); err == nil {
		t.Fatal("forgot an account magpie does not know")
	}
	if err := ForgetAccount("claude", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(accountFolder("claude", "b@example.com")); !os.IsNotExist(err) {
		t.Fatalf("b's sign-in kept: %v", err)
	}
	if rows := remembered(t, "claude"); len(rows) != 1 || rows[0].User != "a@example.com" {
		t.Fatalf("rows: %+v", rows)
	}
}

func TestSetLoginOnRules(t *testing.T) {
	home := accountsHome(t)
	twoClaudeAccounts(t, home)

	if err := SetLoginOn("claude", "a@example.com", false); err == nil {
		t.Fatal("took the signed-in account off")
	}
	if err := SetLoginOn("claude", "a@example.com", true); err != nil {
		t.Fatalf("the signed-in account is always on: %v", err)
	}
	if err := SetLoginOn("claude", "nobody@example.com", true); err == nil {
		t.Fatal("turned on an unknown account")
	}
	if err := SetLoginOn("claude", "b@example.com", false); err != nil {
		t.Fatal(err)
	}
	p, _ := find(All(), "claude")
	if got := p.Alongside(); len(got) != 0 {
		t.Fatalf("alongside after off: %d", len(got))
	}
	if err := SetLoginOn("claude", "b@example.com", true); err != nil {
		t.Fatal(err)
	}
	if got := p.Alongside(); len(got) != 1 || got[0].Account.User != "b@example.com" {
		t.Fatalf("alongside after on: %+v", got)
	}
}

func TestSwitchToUnknownAccountFails(t *testing.T) {
	home := accountsHome(t)
	twoClaudeAccounts(t, home)

	if _, err := SwitchAccount("claude", "nobody@example.com"); err == nil {
		t.Fatal("switched to an unknown account")
	}
	if _, err := SwitchAccount("gemini", "a@example.com"); err == nil || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("unknown agent: %v", err)
	}
	// nothing moved
	agentStore, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if !strings.Contains(string(agentStore), "sk-ant-oat01-a1") {
		t.Fatalf("agent store changed: %s", agentStore)
	}
}

func TestFailedAddPutsThePreviousSignInBack(t *testing.T) {
	home := accountsHome(t)
	signInClaude(t, home, "a", "1")
	fakeLogin(t, func(string) error { return errors.New("browser closed") })

	if _, err := AddAccount("claude"); err == nil {
		t.Fatal("add succeeded without a login")
	}
	agentStore, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if !strings.Contains(string(agentStore), "sk-ant-oat01-a1") {
		t.Fatalf("agent store: %s", agentStore)
	}
	if rows := remembered(t, "claude"); len(rows) != 1 || !rows[0].Current {
		t.Fatalf("rows: %+v", rows)
	}
	if _, err := os.Stat(accountFolder("claude", "a@example.com")); !os.IsNotExist(err) {
		t.Fatalf("a's copy left behind: %v", err)
	}
}

func TestAlongsideFollowsSavedOrder(t *testing.T) {
	home := accountsHome(t)
	twoClaudeAccounts(t, home)
	fakeLogin(t, func(string) error {
		signInClaude(t, home, "c", "1")
		return nil
	})
	if _, err := AddAccount("claude"); err != nil {
		t.Fatal(err)
	}
	// the login added c; a is still the one signed in
	p, ok := find(All(), "claude")
	if !ok || p.Account.User != "a@example.com" {
		t.Fatalf("signed in: %+v %v", p, ok)
	}
	alongside := p.Alongside()
	if len(alongside) != 2 || alongside[0].Account.User != "b@example.com" || alongside[1].Account.User != "c@example.com" {
		t.Fatalf("alongside: %+v", alongside)
	}
	for _, sp := range alongside {
		if sp.Account.ConfigDir() == "" || sp.ID != "claude" || sp.Anthropic != p.Anthropic {
			t.Fatalf("sibling: %+v", sp)
		}
	}
	if p.Account.ConfigDir() != "" {
		t.Fatal("the signed-in account has its own store, not a folder")
	}
}

func TestStoredSignInRefreshesInItsOwnFolder(t *testing.T) {
	home := accountsHome(t)
	twoClaudeAccounts(t, home)

	// b's access token has expired; its refresh rotates it, in b's folder
	folder := accountFolder("claude", "b@example.com")
	writeFile(t, filepath.Join(folder, ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-b-old", "refreshToken": "sk-ant-ort01-b",
			"expiresAt": time.Now().Add(-time.Hour).UnixMilli(), "subscriptionType": "max"},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"sk-ant-oat01-b-new","refresh_token":"sk-ant-ort01-b2","expires_in":3600}`))
	}))
	defer srv.Close()
	claudeTokenURL = srv.URL

	p, _ := find(All(), "claude")
	sib := p.Alongside()
	if len(sib) != 1 {
		t.Fatalf("alongside: %d", len(sib))
	}
	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	if err := sib[0].Sign(context.Background(), req, Anthropic, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer sk-ant-oat01-b-new" {
		t.Fatalf("auth: %q", req.Header.Get("Authorization"))
	}
	var kept map[string]any
	readJSON(filepath.Join(folder, ".credentials.json"), &kept)
	if oa, _ := kept["claudeAiOauth"].(map[string]any); oa["refreshToken"] != "sk-ant-ort01-b2" {
		t.Fatalf("rotated pair not kept: %v", kept)
	}
	// the agent's own sign-in is untouched
	agentStore, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if !strings.Contains(string(agentStore), "sk-ant-oat01-a1") {
		t.Fatalf("agent store changed: %s", agentStore)
	}
}

func TestCodexSwitchKeepsConfigAndOtherFields(t *testing.T) {
	home := accountsHome(t)
	codexSignIn(t, home, "a@example.com", "acct-a")
	config := filepath.Join(home, ".codex", "config.toml")
	if err := os.WriteFile(config, []byte("model = \"gpt-5.5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fakeLogin(t, func(agent string) error {
		codexSignIn(t, home, "b@example.com", "acct-b")
		return nil
	})
	if _, err := AddAccount("codex"); err != nil {
		t.Fatal(err)
	}
	if rows := remembered(t, "codex"); len(rows) != 2 || !rows[0].Current || !rows[1].On {
		t.Fatalf("rows: %+v", rows)
	}

	// a's tokens rotate in the agent's file before the switch
	codexSignIn(t, home, "a@example.com", "acct-a2")
	if _, err := SwitchAccount("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}

	var auth map[string]any
	readJSON(filepath.Join(home, ".codex", "auth.json"), &auth)
	toks, _ := auth["tokens"].(map[string]any)
	if toks["account_id"] != "acct-b" || auth["custom"] != "keep" {
		t.Fatalf("auth after switch: %v", auth)
	}
	if p, ok := find(All(), "codex"); !ok || p.Account.User != "b@example.com" {
		t.Fatalf("codex after switch: %+v %v", p, ok)
	}
	var kept map[string]any
	readJSON(filepath.Join(accountFolder("codex", "a@example.com"), "auth.json"), &kept)
	keptToks, _ := kept["tokens"].(map[string]any)
	if keptToks["account_id"] != "acct-a2" {
		t.Fatalf("a's kept tokens: %v", kept)
	}
	if b, _ := os.ReadFile(config); string(b) != "model = \"gpt-5.5\"\n" {
		t.Fatalf("config.toml changed: %s", b)
	}
}

func TestLoginUsageCachesAndKeepsLastKnown(t *testing.T) {
	home := accountsHome(t)
	twoClaudeAccounts(t, home)

	var mu sync.Mutex
	calls, failB := 0, false
	old := usageFetch
	usageFetch = func(ctx context.Context, a loginAccount) SubscriptionQuota {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if a.User == "b@example.com" && failB {
			return SubscriptionQuota{Provider: "claude", Error: "rate limited"}
		}
		return SubscriptionQuota{Provider: "claude", Windows: []QuotaWindow{{Name: "5 hours", Used: 12}, {Name: "7 days", Used: 40}}}
	}
	t.Cleanup(func() { usageFetch = old })

	got := LoginUsage(context.Background(), "claude")
	if len(got) != 2 || got["b@example.com"].FiveHour != 12 || got["a@example.com"].Weekly != 40 {
		t.Fatalf("usage: %+v", got)
	}
	if calls != 2 {
		t.Fatalf("calls: %d", calls)
	}
	LoginUsage(context.Background(), "claude")
	if calls != 2 {
		t.Fatalf("a minute's answer was not reused: %d calls", calls)
	}

	// the answers are a minute old now, and b's query fails: b keeps what it had
	loginUsageCache.Lock()
	for k := range loginUsageCache.at {
		loginUsageCache.at[k] = time.Now().Add(-2 * time.Minute)
	}
	loginUsageCache.Unlock()
	mu.Lock()
	failB = true
	mu.Unlock()
	got = LoginUsage(context.Background(), "claude")
	if got["b@example.com"].FiveHour != 12 || got["b@example.com"].Error != "" {
		t.Fatalf("b's last known answer lost: %+v", got["b@example.com"])
	}
	if calls != 4 {
		t.Fatalf("calls after the minute: %d", calls)
	}

	if other := LoginUsage(context.Background(), "gemini"); len(other) != 0 {
		t.Fatalf("unsupported agent: %+v", other)
	}
}
