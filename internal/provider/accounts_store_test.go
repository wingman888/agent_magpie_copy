package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// codexLogin writes a fresh ChatGPT login for user into home's Codex auth.json.
// The file carries a field of its own, which a switch must leave alone.
func codexLogin(t *testing.T, home, user, refresh string) {
	t.Helper()
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"custom":    "keep",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": user,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + user}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp, "sub": user}),
			"refresh_token": refresh,
			"account_id":    "acct-" + user,
		},
	})
}

func readAuth(t *testing.T, home string) map[string]any {
	t.Helper()
	var raw map[string]any
	if !readJSON(filepath.Join(home, ".codex", "auth.json"), &raw) {
		t.Fatal("no Codex login on disk")
	}
	return raw
}

// claudeBlob is a Claude Code credential file for one account.
func claudeBlob(t *testing.T, email, access, refresh string, expiresAt int64) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"email": email, "claudeAiOauth": map[string]any{
		"accessToken": access, "refreshToken": refresh, "expiresAt": expiresAt, "subscriptionType": "max"}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// currentProvider is the signed-in account the agent is logged in as.
func currentProvider(t *testing.T, agent string) Provider {
	t.Helper()
	for _, p := range Accounts() {
		if p.Account != nil && p.Account.Agent == agent {
			return p
		}
	}
	t.Fatalf("no %s login", agent)
	return Provider{}
}

func resetLoginUsage(t *testing.T) {
	t.Helper()
	reset := func() {
		loginUsage.Lock()
		loginUsage.at = map[string]time.Time{}
		loginUsage.data = map[string]map[string]SubscriptionQuota{}
		loginUsage.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestCodexSwitchKeepsCredentialsAndOtherFields(t *testing.T) {
	home := signIn(t) // me@example.com is the current login
	resetLoginUsage(t)
	Accounts() // remembers it
	codexLogin(t, home, "you@example.com", "r-you")
	Accounts() // the second account is 同时生效 from the start

	infos, err := RememberedAccounts("codex")
	if err != nil || len(infos) != 2 || infos[0].User != "you@example.com" || !infos[0].Current ||
		infos[1].User != "me@example.com" || infos[1].On {
		t.Fatalf("listing = %+v, %v", infos, err)
	}

	// the agent rotates its refresh token while it is the current login
	raw := readAuth(t, home)
	raw["tokens"].(map[string]any)["refresh_token"] = "r-you-rotated"
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), raw)

	if err := SwitchAccount("codex", "ME@example.com"); err != nil {
		t.Fatal(err)
	}
	raw = readAuth(t, home)
	if raw["custom"] != "keep" {
		t.Fatalf("switch changed a field it should not: %v", raw)
	}
	if email := claimString(jwtClaims(raw["tokens"].(map[string]any)["id_token"].(string)), "email"); email != "me@example.com" {
		t.Fatalf("agent is signed in as %s after switch", email)
	}

	you, ok := findRemembered("codex", "you@example.com")
	if !ok {
		t.Fatal("replaced account was forgotten")
	}
	var saved map[string]any
	if json.Unmarshal(you.Creds, &saved) != nil || saved["custom"] != "keep" ||
		saved["tokens"].(map[string]any)["refresh_token"] != "r-you-rotated" {
		t.Fatalf("replaced account lost its latest credentials: %s", you.Creds)
	}
	// the replaced account takes the 同时生效 state the target had (off)
	if you.On {
		t.Fatal("replaced account should inherit the target's 同时生效 state")
	}

	if err := ForgetAccount("codex", "me@example.com"); err == nil {
		t.Fatal("the current login was forgotten")
	}
	if err := SwitchAccount("codex", "nobody@example.com"); err == nil {
		t.Fatal("switched to an account magpie does not remember")
	}
	if err := SwitchAccount("gemini", "me@example.com"); !errors.Is(err, ErrUnknownAgent) {
		t.Fatalf("unknown agent: %v", err)
	}
	if err := ForgetAccount("codex", "you@example.com"); err != nil {
		t.Fatal(err)
	}
	if infos, _ := RememberedAccounts("codex"); len(infos) != 1 {
		t.Fatalf("after forget: %+v", infos)
	}
	if st, err := os.Stat(accountsPath()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("accounts.json not 0600: %v %v", st, err)
	}
}

func TestSetLoginOnAndCandidateOrder(t *testing.T) {
	home := signIn(t)
	resetLoginUsage(t)
	Accounts()
	codexLogin(t, home, "you@example.com", "r-you")
	Accounts() // you is current

	if err := SetLoginOn("codex", "you@example.com", false); err == nil {
		t.Fatal("the current login was turned off")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("unknown account accepted")
	}
	if err := SetLoginOn("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	cur := currentProvider(t, "codex")
	cands := Candidates(cur, Responses, "gpt-5.5")
	if len(cands) != 2 || cands[0].Account.User != "you@example.com" || cands[1].Account.User != "me@example.com" {
		t.Fatalf("candidates = %+v", cands)
	}
	if err := SetLoginOn("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if n := len(Candidates(cur, Responses, "gpt-5.5")); n != 1 {
		t.Fatalf("an account that is off is still a candidate: %d", n)
	}
}

func TestAddAccountRemembersTheNewLogin(t *testing.T) {
	home := signIn(t)
	Accounts() // remembers me@example.com
	other := t.TempDir()
	codexLogin(t, other, "you@example.com", "r-you")

	oldLogin := loginCommand
	t.Cleanup(func() { loginCommand = oldLogin })
	loginCommand = func(agent string) (*exec.Cmd, error) {
		// stands in for `codex login`: the browser step leaves a new login behind
		return exec.Command("cp", filepath.Join(other, ".codex", "auth.json"), filepath.Join(home, ".codex", "auth.json")), nil
	}
	user, err := AddAccount("codex")
	if err != nil || user != "you@example.com" {
		t.Fatalf("AddAccount = %q, %v", user, err)
	}
	infos, _ := RememberedAccounts("codex")
	if len(infos) != 2 || infos[0].User != "you@example.com" || !infos[0].Current ||
		infos[1].User != "me@example.com" || !infos[1].On {
		t.Fatalf("after add: %+v", infos)
	}
}

func TestStoredClaudeAccountRefreshesItsOwnCopy(t *testing.T) {
	signIn(t)
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		seen = body["refresh_token"]
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "r-new", "expires_in": 3600})
	}))
	defer srv.Close()
	claudeTokenURL = srv.URL // restored by isolate

	remember(&Account{Agent: "claude", User: "you@example.com", Plan: "max",
		creds: claudeBlob(t, "you@example.com", "old-access", "r-old", 1000)})
	tok, err := storedClaudeToken(context.Background(), "you@example.com")
	if err != nil || tok != "new-access" || seen != "r-old" {
		t.Fatalf("token %q, %v, refreshed with %q", tok, err, seen)
	}
	r, _ := findRemembered("claude", "you@example.com")
	c, ok := parseClaudeCredentials(r.Creds)
	if !ok || c.OAuth.RefreshToken != "r-new" {
		t.Fatalf("rotated refresh token not kept: %+v", c.OAuth)
	}
}

func TestClaudeSwitchTouchesOnlyTheLogin(t *testing.T) {
	signIn(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	// stands in for `claude auth status --json`: names the account by its access token
	script := filepath.Join(t.TempDir(), "claude")
	sh := `#!/bin/sh
t=$(sed -n 's/.*"accessToken": *"\([^"]*\)".*/\1/p' "$CLAUDE_CONFIG_DIR/.credentials.json")
case "$t" in
me-*) e=me@example.com ;;
*) e=you@example.com ;;
esac
printf '{"loggedIn": true, "email": "%s", "subscriptionType": "max"}\n' "$e"
`
	if err := os.WriteFile(script, []byte(sh), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return script }
	forgetClaudeStatus()

	fresh := time.Now().Add(time.Hour).UnixMilli()
	login := func(access, refresh string) {
		writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{
			"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh, "expiresAt": fresh, "subscriptionType": "max"},
			"mcpOAuth":      map[string]any{"srv": map[string]any{"token": "keep"}},
		})
		forgetClaudeCredential()
		forgetClaudeStatus()
	}
	login("me-access", "r-me")
	Accounts() // remembers me@example.com
	remember(&Account{Agent: "claude", User: "you@example.com", Plan: "max",
		creds: claudeBlob(t, "you@example.com", "you-access", "r-you", fresh)})

	login("me-access", "r-me-2") // the agent rotated the refresh token
	if err := SwitchAccount("claude", "YOU@example.com"); err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	readJSON(filepath.Join(dir, ".credentials.json"), &raw)
	if oauth, _ := raw["claudeAiOauth"].(map[string]any); oauth["accessToken"] != "you-access" {
		t.Fatalf("login not switched: %v", raw["claudeAiOauth"])
	}
	if raw["mcpOAuth"] == nil {
		t.Fatal("switch dropped the agent's other fields")
	}
	me, _ := findRemembered("claude", "me@example.com")
	c, ok := parseClaudeCredentials(me.Creds)
	if !ok || c.OAuth.RefreshToken != "r-me-2" {
		t.Fatalf("replaced account lost its latest credentials: %+v", c.OAuth)
	}
	infos, _ := RememberedAccounts("claude")
	if len(infos) != 2 || infos[0].User != "you@example.com" || !infos[0].Current {
		t.Fatalf("after switch: %+v", infos)
	}
}

func TestLoginUsageKeepsLastKnownResult(t *testing.T) {
	home := signIn(t)
	resetLoginUsage(t)
	Accounts()
	codexLogin(t, home, "you@example.com", "r-you")
	Accounts() // you is current; me is remembered

	var calls atomic.Int32
	var failYou atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/wham/usage") {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("chatgpt-account-id") == "acct-you@example.com" && failYou.Load() == 1 {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":12.5,"limit_window_seconds":18000,"reset_after_seconds":60},` +
			`"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_after_seconds":600}}}`))
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	ctx := context.Background()
	res := LoginUsage(ctx, "codex")
	if len(res) != 2 || len(res["you@example.com"].Windows) != 2 || res["you@example.com"].Windows[0].Used != 12.5 ||
		res["me@example.com"].Windows[1].Used != 40 {
		t.Fatalf("usage = %+v", res)
	}
	before := calls.Load()
	LoginUsage(ctx, "codex")
	if calls.Load() != before {
		t.Fatal("usage was not reused within a minute")
	}

	failYou.Store(1)
	loginUsage.Lock()
	loginUsage.at = map[string]time.Time{}
	loginUsage.Unlock()
	res = LoginUsage(ctx, "codex")
	if q := res["you@example.com"]; q.Error != "" || len(q.Windows) == 0 || q.Windows[0].Used != 12.5 {
		t.Fatalf("a failed query lost the last known usage: %+v", q)
	}
	if got := LoginUsage(ctx, "gemini"); got == nil || len(got) != 0 {
		t.Fatalf("unsupported agent usage = %+v", got)
	}
}
