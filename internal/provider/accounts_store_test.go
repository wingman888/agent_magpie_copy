package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// savedHome is a fresh home with nothing signed in, and the magpie config
// directory inside it.
func savedHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

// codexLoginAs writes the Codex login of email into the home's auth.json. The
// access token is valid for an hour, so it is used without a refresh.
func codexLoginAs(t *testing.T, home, email, refresh string, extra map[string]any) {
	t.Helper()
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  fakeJWT(map[string]any{"sub": email, "exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": refresh,
			"account_id":    "acct-" + email,
		},
	}
	for k, v := range extra {
		auth[k] = v
	}
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), auth)
}

// claudeLoginAs writes a Claude Code login of email; the fake claude CLI
// reports the email from the access token.
func claudeLoginAs(t *testing.T, home, email, refresh string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "at-" + email, "refreshToken": refresh,
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max"},
		"mcpOAuth": map[string]any{"server": "kept"},
	})
	forgetClaudeCredential()
	forgetClaudeStatus()
}

// fakeClaudeCLI answers `claude auth status --json` for the login in the home.
const fakeClaudeCLI = `#!/bin/sh
t=$(sed -n 's/.*"accessToken": *"at-\([^"]*\)".*/\1/p' "$HOME/.claude/.credentials.json" | head -n 1)
printf '{"loggedIn": true, "email": "%s", "subscriptionType": "max"}\n' "$t"
`

func readMap(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// listSaved lists an agent's remembered accounts, failing the test on error.
func listSaved(t *testing.T, agent string) []SavedAccount {
	t.Helper()
	list, err := SavedAccounts(agent)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// storedFile is where magpie keeps a remembered account's credential.
func storedFile(t *testing.T, agent, user string) string {
	t.Helper()
	x := loadIndex(agent)
	i := x.find(user)
	if i < 0 {
		t.Fatalf("%s account %s is not remembered", agent, user)
	}
	return credPath(agent, x.Accounts[i])
}

// storedRefresh is the refresh token of a remembered account's credential.
func storedRefresh(t *testing.T, agent, user string) string {
	t.Helper()
	m := readMap(t, storedFile(t, agent, user))
	if agent == "claude" {
		return m["claudeAiOauth"].(map[string]any)["refreshToken"].(string)
	}
	return m["tokens"].(map[string]any)["refresh_token"].(string)
}

// signedInRefresh is the refresh token the agent itself holds right now.
func signedInRefresh(t *testing.T, home, agent string) string {
	t.Helper()
	if agent == "claude" {
		return readMap(t, filepath.Join(home, ".claude", ".credentials.json"))["claudeAiOauth"].(map[string]any)["refreshToken"].(string)
	}
	return readMap(t, filepath.Join(home, ".codex", "auth.json"))["tokens"].(map[string]any)["refresh_token"].(string)
}

func TestCodexSavedAccountsSwitchAndForget(t *testing.T) {
	home := savedHome(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	extra := map[string]any{"OPENAI_API_KEY": "sk-keep"}

	codexLoginAs(t, home, "a@example.com", "rA", extra)
	All() // remembers the account the agent is signed in to
	if list := listSaved(t, "codex"); len(list) != 1 || !list[0].Current || list[0].On {
		t.Fatalf("first sign-in: got %+v", list)
	}

	codexLoginAs(t, home, "b@example.com", "rB", extra)
	list := listSaved(t, "codex")
	if len(list) != 2 || list[0].On || list[0].Current || !list[1].Current || !list[1].On {
		t.Fatalf("second sign-in should be on at the same time: got %+v", list)
	}

	if err := SwitchAccount("codex", "A@Example.com"); err != nil {
		t.Fatalf("switch (email is case-insensitive): %v", err)
	}
	if got := signedInRefresh(t, home, "codex"); got != "rA" {
		t.Fatalf("agent signed in with refresh %q after switch, want rA", got)
	}
	if got := storedRefresh(t, "codex", "b@example.com"); got != "rB" {
		t.Fatalf("replaced account kept refresh %q, want rB", got)
	}
	if m := readMap(t, authPath); m["OPENAI_API_KEY"] != "sk-keep" {
		t.Fatalf("switch changed another field of auth.json: %v", m)
	}

	// the agent refreshes a's token while it is signed in; the switch keeps that
	codexLoginAs(t, home, "a@example.com", "rA2", extra)
	if err := SwitchAccount("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := storedRefresh(t, "codex", "a@example.com"); got != "rA2" {
		t.Fatalf("replaced account lost its latest credential: refresh %q, want rA2", got)
	}
	if got := signedInRefresh(t, home, "codex"); got != "rB" {
		t.Fatalf("agent signed in with refresh %q, want rB", got)
	}

	if err := ForgetAccount("codex", "b@example.com"); err == nil {
		t.Fatal("forgetting the signed-in account must be refused")
	}
	if err := ForgetAccount("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(storedFile(t, "codex", "b@example.com")); err != nil {
		t.Fatal("the signed-in account's copy must stay")
	}
	if len(listSaved(t, "codex")) != 1 {
		t.Fatal("forgotten account is still listed")
	}
	if err := ForgetAccount("codex", "nobody@example.com"); err == nil {
		t.Fatal("forgetting an unknown account must fail")
	}
	if err := SwitchAccount("codex", "nobody@example.com"); err == nil {
		t.Fatal("switching to an unknown account must fail")
	}
	if err := SwitchAccount("gemini", "a@example.com"); err == nil {
		t.Fatal("an agent without saved sign-ins must be refused")
	}
}

func TestSavedAccountsAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not checked on windows")
	}
	home := savedHome(t)
	codexLoginAs(t, home, "a@example.com", "rA", nil)
	All()
	dir, err := os.Stat(storeDir("codex"))
	if err != nil {
		t.Fatal(err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("store directory mode %v, want 0700", dir.Mode().Perm())
	}
	for _, f := range []string{storedFile(t, "codex", "a@example.com"), filepath.Join(storeDir("codex"), "accounts.json")} {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v, want 0600", f, st.Mode().Perm())
		}
	}
}

func TestSetLoginOnRules(t *testing.T) {
	home := savedHome(t)
	authPath := filepath.Join(home, ".codex", "auth.json")
	codexLoginAs(t, home, "a@example.com", "rA", nil)
	All()
	codexLoginAs(t, home, "b@example.com", "rB", nil)
	before, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}

	// b is the account signed in now (the second login), a is remembered
	if err := SetLoginOn("codex", "b@example.com", false); err == nil {
		t.Fatal("turning off the signed-in account must be refused")
	}
	if err := SetLoginOn("codex", "b@example.com", true); err != nil {
		t.Fatalf("the signed-in account is already on: %v", err)
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("an unknown account must be refused")
	}
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	if list := listSaved(t, "codex"); !list[0].On || !list[1].On {
		t.Fatalf("both accounts should be on: %+v", list)
	}
	if err := SetLoginOn("codex", "a@example.com", false); err != nil {
		t.Fatal(err)
	}
	if list := listSaved(t, "codex"); list[0].On {
		t.Fatalf("a should be off: %+v", list)
	}
	after, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("keeping an account on at the same time must not write the agent's credential store")
	}
}

func TestSignedInProvidersOrderAndOwnLogin(t *testing.T) {
	home := savedHome(t)
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"} {
		codexLoginAs(t, home, email, "r-"+email, nil)
		All()
	}
	// back to a: b and c were on (second and third seen), d goes off with a's state
	if err := SwitchAccount("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	var cur Provider
	for _, p := range All() {
		if p.ID == "codex" {
			cur = p
		}
	}
	if cur.Account == nil {
		t.Fatal("codex is not listed as signed in")
	}
	got := SignedInProviders(cur)
	want := []string{"a@example.com", "b@example.com", "c@example.com"}
	if len(got) != len(want) {
		t.Fatalf("candidates %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Account.User != w {
			t.Fatalf("candidate %d is %s, want %s", i, got[i].Account.User, w)
		}
	}

	// a candidate is signed with its own login, not the one the agent holds
	req, _ := http.NewRequest(http.MethodPost, "https://example.invalid/v1/responses", nil)
	if err := got[1].Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	wantTok := readMap(t, storedFile(t, "codex", "b@example.com"))["tokens"].(map[string]any)["access_token"].(string)
	if req.Header.Get("Authorization") != "Bearer "+wantTok {
		t.Fatal("the second candidate was not signed with its own access token")
	}
}

func TestClaudeSavedAccountsSwitch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude CLI is a shell script")
	}
	home := savedHome(t)
	exe := filepath.Join(home, "fake-claude")
	if err := os.WriteFile(exe, []byte(fakeClaudeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return exe }

	claudeLoginAs(t, home, "a@example.com", "rA")
	All()
	claudeLoginAs(t, home, "b@example.com", "rB")
	if list := listSaved(t, "claude"); len(list) != 2 || !list[1].Current || list[0].Current {
		t.Fatalf("claude accounts: got %+v", list)
	}

	if err := SwitchAccount("claude", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := signedInRefresh(t, home, "claude"); got != "rA" {
		t.Fatalf("claude signed in with refresh %q, want rA", got)
	}
	creds := readMap(t, filepath.Join(home, ".claude", ".credentials.json"))
	if _, ok := creds["mcpOAuth"]; !ok {
		t.Fatal("switch dropped another field of Claude Code's credentials")
	}
	if got := storedRefresh(t, "claude", "b@example.com"); got != "rB" {
		t.Fatalf("replaced claude account kept refresh %q, want rB", got)
	}
	if err := ForgetAccount("claude", "a@example.com"); err == nil {
		t.Fatal("forgetting the signed-in claude account must be refused")
	}
	if err := ForgetAccount("claude", "b@example.com"); err != nil {
		t.Fatal(err)
	}
}
