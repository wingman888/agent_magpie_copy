package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loginHome isolates a test in a temp home and config dir, with no real
// Claude Code login or keychain in reach.
func loginHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	resetLoginUsage()
	return home
}

func resetLoginUsage() {
	loginUsageCache.Lock()
	loginUsageCache.data, loginUsageCache.at = nil, nil
	loginUsageCache.Unlock()
}

// codexSignIn makes the Codex CLI sign in as email with the given access
// token, then lets magpie notice it, the way a start-up does.
func codexSignIn(t *testing.T, home, email, access string, extra map[string]any) {
	t.Helper()
	exp := float64(time.Now().Add(time.Hour).Unix())
	auth := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp, "tag": access}),
			"refresh_token": "refresh-" + access,
			"account_id":    "acct-" + email,
		},
		"last_refresh": "2026-01-01T00:00:00Z",
	}
	for k, v := range extra {
		auth[k] = v
	}
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), auth)
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
}

// tagOf names the Codex access token a test gave a login ("A1", "B2", …).
func tagOf(tok string) string { return claimString(jwtClaims(tok), "tag") }

// accessOf is the raw access token a stored login holds.
func accessOf(t *testing.T, agent, user string) string {
	t.Helper()
	for _, l := range loadLogins().Logins {
		if l.Agent == agent && strings.EqualFold(l.User, user) {
			var c struct {
				Tokens struct {
					AccessToken string `json:"access_token"`
				} `json:"tokens"`
				AccessToken string `json:"accessToken"`
			}
			if err := json.Unmarshal(l.Creds, &c); err != nil {
				t.Fatal(err)
			}
			if c.AccessToken != "" {
				return c.AccessToken
			}
			return c.Tokens.AccessToken
		}
	}
	t.Fatalf("no saved %s login for %s", agent, user)
	return ""
}

func TestCodexSwitchKeepsLatestCredsAndOtherFields(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@x.com", "A1", nil)
	codexSignIn(t, home, "b@x.com", "B1", nil)
	if !findOn(t, "codex", "b@x.com") || findOn(t, "codex", "a@x.com") {
		t.Fatal("the second login should be on and the first off")
	}
	// the agent rotated A's tokens while signed in as A; switching must keep them
	codexSignIn(t, home, "a@x.com", "A2", map[string]any{"other": "keep"})
	switched, err := SwitchLogin("codex", "B@X.com")
	if err != nil || !switched {
		t.Fatalf("switch: %v %v", switched, err)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	var auth map[string]any
	json.Unmarshal(b, &auth)
	toks := auth["tokens"].(map[string]any)
	if tagOf(toks["access_token"].(string)) != "B1" {
		t.Errorf("agent did not switch to B: %v", toks["access_token"])
	}
	if auth["other"] != "keep" {
		t.Errorf("switch dropped an unrelated field: %v", auth)
	}
	if got := tagOf(accessOf(t, "codex", "a@x.com")); got != "A2" {
		t.Errorf("outgoing login lost its latest token: %s", got)
	}
	if !findOn(t, "codex", "a@x.com") {
		t.Error("the outgoing login should inherit the on-state of the one it gave way to")
	}
	if switched, err := SwitchLogin("codex", "b@x.com"); err != nil || switched {
		t.Errorf("switching to the current login should be a no-op: %v %v", switched, err)
	}
	if _, err := SwitchLogin("codex", "nobody@x.com"); err == nil {
		t.Error("switching to an unknown account did not fail")
	}
}

// findOn reports whether a remembered login is switched on.
func findOn(t *testing.T, agent, user string) bool {
	t.Helper()
	for _, l := range Logins(agent) {
		if strings.EqualFold(l.User, user) {
			return l.On
		}
	}
	t.Fatalf("no login %s", user)
	return false
}

func TestForgetAndSetLoginOn(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@x.com", "A1", nil)
	codexSignIn(t, home, "b@x.com", "B1", nil)
	// B is signed in now, so A is the one to forget and B the one that cannot be
	if err := ForgetLogin("codex", "b@x.com"); err == nil {
		t.Error("forgot the account that is signed in")
	}
	if err := SetLoginOn("codex", "b@x.com", false); err == nil {
		t.Error("switched off the account that is signed in")
	}
	if err := SetLoginOn("codex", "nobody@x.com", true); err == nil {
		t.Error("switch on of an unknown account did not fail")
	}
	if err := SetLoginOn("codex", "a@x.com", false); err != nil {
		t.Fatal(err)
	}
	if findOn(t, "codex", "a@x.com") {
		t.Error("a was not switched off")
	}
	if err := ForgetLogin("codex", "A@x.com"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := Logins("codex"); len(got) != 1 || !got[0].Current {
		t.Errorf("after forgetting a, logins = %+v", got)
	}
	if err := ForgetLogin("codex", "a@x.com"); err == nil {
		t.Error("forgetting twice did not fail")
	}
	if err := ForgetLogin("gemini", "a@x.com"); err == nil {
		t.Error("unknown agent did not fail")
	}
}

func TestStoreIsPrivate(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@x.com", "A1", nil)
	st, err := os.Stat(loginsPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("accounts.json mode %v, want 0600", st.Mode().Perm())
	}
}

func TestGatewayCandidatesCurrentFirst(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@x.com", "A1", nil)
	codexSignIn(t, home, "b@x.com", "B1", nil)
	codexSignIn(t, home, "c@x.com", "C1", nil)
	codexSignIn(t, home, "d@x.com", "D1", nil)
	// A is the one signed in again; D is switched off while it is not current
	codexSignIn(t, home, "a@x.com", "A1", nil)
	if err := SetLoginOn("codex", "d@x.com", false); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "codex")
	if !ok || p.Account == nil {
		t.Fatal("codex provider missing")
	}
	var users []string
	for _, v := range p.AccountViews() {
		users = append(users, v.Account.User)
	}
	if strings.Join(users, ",") != "a@x.com,b@x.com,c@x.com" {
		t.Fatalf("candidates = %v, want current first then saved order of the on logins", users)
	}
	// each candidate signs with its own credentials
	for _, v := range p.AccountViews()[1:] {
		req, _ := http.NewRequest(http.MethodPost, "https://example.test/", nil)
		if err := v.Sign(context.Background(), req, "", nil); err != nil {
			t.Fatalf("sign %s: %v", v.Account.User, err)
		}
		want := strings.ToUpper(strings.Split(v.Account.User, "@")[0]) + "1"
		if got := tagOf(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")); got != want {
			t.Errorf("%s signed with token %q, want %q", v.Account.User, got, want)
		}
	}
}

func TestClaudeSwitchKeepsOtherKeysAndLatestTokens(t *testing.T) {
	home := loginHome(t)
	dir := filepath.Join(home, ".claude")
	who := filepath.Join(home, "who.json")
	script := filepath.Join(home, "claude")
	// the stub signs in as whoever the credential file names, as Claude Code does
	stub := "#!/bin/sh\nif grep -q '\"A1\"' " + filepath.Join(dir, ".credentials.json") + "; then echo '{\"loggedIn\":true,\"email\":\"a@x.com\",\"subscriptionType\":\"max\"}'; else cat " + who + "; fi\n"
	if err := os.WriteFile(script, []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return script }
	setWho := func(email string) {
		os.WriteFile(who, []byte(`{"loggedIn":true,"email":"`+email+`","subscriptionType":"max"}`), 0o600)
		forgetClaudeStatus()
	}
	writeCreds := func(token string) {
		writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{
			"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": "r-" + token,
				"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max"},
			"mcpOAuth": map[string]any{"x": 1},
		})
	}
	setWho("a@x.com")
	writeCreds("A1")
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	setWho("b@x.com")
	writeCreds("B1")
	if err := RememberLogins(); err != nil {
		t.Fatal(err)
	}
	// B rotated while signed in; switching to A must keep that
	writeCreds("B2")
	setWho("b@x.com")
	if _, err := SwitchLogin("claude", "a@x.com"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	var got map[string]any
	json.Unmarshal(b, &got)
	oauth := got["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "A1" {
		t.Errorf("switch did not sign in as A: %v", oauth["accessToken"])
	}
	if mcp, _ := got["mcpOAuth"].(map[string]any); mcp["x"] != float64(1) {
		t.Errorf("switch dropped mcpOAuth: %v", got)
	}
	if accessOf(t, "claude", "b@x.com") != "B2" {
		t.Errorf("outgoing Claude login lost its latest token")
	}
	// the agent's file stays private
	st, _ := os.Stat(filepath.Join(dir, ".credentials.json"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode %v, want 0600", st.Mode().Perm())
	}
}

func TestAddLoginRemembersTheNewAccount(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@x.com", "A1", nil)
	src := filepath.Join(home, "c-auth.json")
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, src, map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     fakeJWT(map[string]any{"email": "c@x.com"}),
		"access_token": "C1." + fakeJWT(map[string]any{"exp": exp}), "refresh_token": "r", "account_id": "acct-c"}})
	loginCommand = func(agent string) (*exec.Cmd, error) {
		return exec.Command("cp", src, filepath.Join(home, ".codex", "auth.json")), nil
	}
	t.Cleanup(func() { loginCommand = defaultLoginCommand })
	user, err := AddLogin("codex")
	if err != nil || user != "c@x.com" {
		t.Fatalf("AddLogin = %q, %v", user, err)
	}
	if !findOn(t, "codex", "c@x.com") {
		t.Error("the second account should start switched on")
	}
	if tagOf(accessOf(t, "codex", "a@x.com")) != "A1" {
		t.Error("the account signed in before was not kept")
	}
}

// defaultLoginCommand is the production value of loginCommand, kept for cleanup.
var defaultLoginCommand = loginCommand

func TestLoginUsageCachesAndKeepsLastKnown(t *testing.T) {
	home := loginHome(t)
	codexSignIn(t, home, "a@x.com", "A1", nil)
	codexSignIn(t, home, "b@x.com", "B1", nil)
	calls := 0
	failB := false
	old := usageFetch
	usageFetch = func(_ context.Context, l savedLogin) (SubscriptionQuota, error) {
		calls++
		if l.User == "b@x.com" && failB {
			return SubscriptionQuota{}, fakeErr("boom")
		}
		v := 10.0
		if strings.HasPrefix(strings.ToLower(l.User), "b") {
			v = 40
		}
		return SubscriptionQuota{Provider: l.Agent, FiveHour: v, Weekly: v + 5, Windows: []QuotaWindow{}}, nil
	}
	t.Cleanup(func() { usageFetch = old })

	got := LoginUsage(context.Background(), "codex")
	if len(got) != 2 || got["a@x.com"].FiveHour != 10 || got["b@x.com"].Weekly != 45 {
		t.Fatalf("usage = %+v", got)
	}
	LoginUsage(context.Background(), "codex")
	if calls != 2 {
		t.Errorf("result within a minute was not reused: %d fetches", calls)
	}
	// a minute later, B cannot be asked: its last known result stands
	loginUsageCache.Lock()
	loginUsageCache.at["codex"] = time.Time{}
	loginUsageCache.Unlock()
	failB = true
	got = LoginUsage(context.Background(), "codex")
	if got["b@x.com"].Error != "" || got["b@x.com"].FiveHour != 40 {
		t.Errorf("failed login lost its last known usage: %+v", got["b@x.com"])
	}
	if len(LoginUsage(context.Background(), "gemini")) != 0 {
		t.Error("an unsupported agent returned usage")
	}
}

// fakeErr is a plain error for the fake usage fetch.
type fakeErr string

func (e fakeErr) Error() string { return string(e) }
