package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// codexHome sets up a temp home with no sign-in yet.
func codexHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// codexAs writes the Codex sign-in of one ChatGPT account into the agent's own
// auth.json and returns its access token.
func codexAs(t *testing.T, home, email, acct, refresh string, expired bool) string {
	t.Helper()
	exp := time.Now().Add(time.Hour).Unix()
	if expired {
		exp = time.Now().Add(-time.Hour).Unix()
	}
	access := fakeJWT(map[string]any{"exp": float64(exp), "who": email})
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": acct}}),
			"access_token": access, "refresh_token": refresh, "account_id": acct,
		},
		"last_refresh":  "2026-01-01T00:00:00Z",
		"other_setting": "keep me",
	})
	return access
}

// claudeAs writes the Claude Code sign-in of one account: the credentials file,
// and the state file that names the account and holds other settings.
func claudeAs(t *testing.T, home, email, access, refresh string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": access, "refreshToken": refresh,
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max", "scopes": []string{"user:inference"}},
		"mcpOAuth": map[string]any{"plugin:slack": map[string]any{"serverName": "Slack"}},
	})
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{
		"numStartups":  7,
		"projects":     map[string]any{"/work": map[string]any{"allowedTools": []string{"Bash"}}},
		"oauthAccount": map[string]any{"emailAddress": email, "accountUuid": "uuid-" + email},
	})
	forgetClaudeCredential()
	forgetClaudeStatus()
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	var m map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func tokenOf(m map[string]any, key string) string {
	t, _ := m["tokens"].(map[string]any)
	s, _ := t[key].(string)
	return s
}

func loginOf(t *testing.T, agent, user string) Login {
	t.Helper()
	for _, l := range Logins(agent) {
		if l.User == user {
			return l
		}
	}
	t.Fatalf("no remembered %s account %s", agent, user)
	return Login{}
}

// twoCodex signs in A, then B, leaving B as the current sign-in. A is remembered
// first, so B is on by default.
func twoCodex(t *testing.T, home string) (accessA, accessB string) {
	t.Helper()
	accessA = codexAs(t, home, "a@example.com", "acct-a", "ra1", false)
	Logins("codex")
	accessB = codexAs(t, home, "b@example.com", "acct-b", "rb1", false)
	Logins("codex")
	return accessA, accessB
}

func TestCodexSwitchKeepsLatestAndOtherFields(t *testing.T) {
	home := codexHome(t)
	accessA, _ := twoCodex(t, home)
	if b := loginOf(t, "codex", "b@example.com"); !b.Current || !b.On {
		t.Fatalf("B should be current and on: %+v", b)
	}
	if a := loginOf(t, "codex", "a@example.com"); a.On {
		t.Fatalf("the first account should not be on by default: %+v", a)
	}

	// B's sign-in is rotated by the agent while B is current; the switch must keep that
	codexAs(t, home, "b@example.com", "acct-b", "rb2", false)
	if err := SwitchLogin("codex", "A@Example.com"); err != nil {
		t.Fatal(err)
	}

	auth := readJSONFile(t, filepath.Join(home, ".codex", "auth.json"))
	if tokenOf(auth, "access_token") != accessA || auth["other_setting"] != "keep me" {
		t.Fatalf("auth.json after switch: %v", auth)
	}
	if a := loginOf(t, "codex", "a@example.com"); !a.Current {
		t.Fatalf("A should be current: %+v", a)
	}
	// B was replaced: it keeps its latest sign-in and takes over A's on state (off)
	b := loginOf(t, "codex", "b@example.com")
	if b.Current || b.On {
		t.Fatalf("B should be off and not current: %+v", b)
	}
	stored, _ := storedLogin("codex", "b@example.com")
	if tokenOf(stored.Cred, "refresh_token") != "rb2" {
		t.Fatalf("B's latest refresh token was not kept: %v", stored.Cred)
	}

	// the remembered accounts file is for this user only
	fi, err := os.Stat(loginsPath())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("accounts file mode %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(home, ".codex", "auth.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode %v, want 0600", fi.Mode().Perm())
	}
}

func TestForgetAndOnRules(t *testing.T) {
	home := codexHome(t)
	twoCodex(t, home)

	if err := ForgetLogin("codex", "b@example.com"); err == nil {
		t.Fatal("forgetting the account signed in now must be refused")
	}
	if err := SetLoginOn("codex", "b@example.com", false); err == nil {
		t.Fatal("the account signed in now cannot be turned off")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("an unknown account must be an error")
	}
	if err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switching to an unknown account must be an error")
	}
	if err := SwitchLogin("nope", "a@example.com"); err == nil {
		t.Fatal("an unknown agent must be an error")
	}

	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	if a := loginOf(t, "codex", "a@example.com"); !a.On {
		t.Fatalf("A should be on: %+v", a)
	}
	if err := ForgetLogin("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if ls := Logins("codex"); len(ls) != 1 || ls[0].User != "b@example.com" {
		t.Fatalf("after forgetting A: %+v", ls)
	}
}

func TestOnAccountServesWithoutWritingAgentStore(t *testing.T) {
	home := codexHome(t)
	accessA, accessB := twoCodex(t, home)

	p, ok := find(All(), "codex")
	if !ok {
		t.Fatal("no codex provider")
	}
	// B is current and A is remembered but not on: only B serves
	if targets := p.LoginTargets(); len(targets) != 1 || targets[0].Account.User != "b@example.com" {
		t.Fatalf("targets before A is on: %+v", targets)
	}
	// once A is on it serves after the current login, in the order remembered
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	p, _ = find(All(), "codex")
	targets := p.LoginTargets()
	if len(targets) != 2 || targets[0].Account.User != "b@example.com" || targets[1].Account.User != "a@example.com" {
		t.Fatalf("targets: %+v", targets)
	}

	// signing with the other account uses its own credentials ...
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := targets[1].Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+accessA || req.Header.Get("chatgpt-account-id") != "acct-a" {
		t.Fatalf("headers: %v", req.Header)
	}
	// ... and never writes them into the agent's own file
	auth := readJSONFile(t, filepath.Join(home, ".codex", "auth.json"))
	if tokenOf(auth, "access_token") != accessB {
		t.Fatalf("the agent's sign-in changed: %v", auth)
	}
}

func TestStoredCodexRefreshStaysInStore(t *testing.T) {
	home := codexHome(t)
	accessA := codexAs(t, home, "a@example.com", "acct-a", "ra1", false)
	Logins("codex")
	codexAs(t, home, "b@example.com", "acct-b", "rb1", true) // expired
	Logins("codex")
	codexAs(t, home, "a@example.com", "acct-a", "ra1", false) // back to A
	Logins("codex")
	if b := loginOf(t, "codex", "b@example.com"); !b.On {
		t.Fatalf("B should be on: %+v", b)
	}

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["refresh_token"] != "rb1" {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-b", "refresh_token": "rb-new"})
	}))
	defer srv.Close()
	old := codexTokenURL
	codexTokenURL = srv.URL
	t.Cleanup(func() { codexTokenURL = old })

	p, _ := find(All(), "codex")
	targets := p.LoginTargets()
	if len(targets) != 2 {
		t.Fatalf("targets: %+v", targets)
	}
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := targets[1].Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 || req.Header.Get("Authorization") != "Bearer fresh-b" {
		t.Fatalf("refresh: hits=%d headers=%v", hits.Load(), req.Header)
	}
	// the rotated pair stays with the remembered account; the agent's file is untouched
	stored, _ := storedLogin("codex", "b@example.com")
	if tokenOf(stored.Cred, "refresh_token") != "rb-new" {
		t.Fatalf("stored refresh: %v", stored.Cred)
	}
	auth := readJSONFile(t, filepath.Join(home, ".codex", "auth.json"))
	if tokenOf(auth, "access_token") != accessA || tokenOf(auth, "refresh_token") != "ra1" {
		t.Fatalf("agent's file changed: %v", auth)
	}
}

func TestAddLoginRemembersSecondAccountOn(t *testing.T) {
	home := codexHome(t)
	codexAs(t, home, "a@example.com", "acct-a", "ra1", false)
	Logins("codex")

	old := loginCommand
	loginCommand = func(agent string) error {
		codexAs(t, home, "b@example.com", "acct-b", "rb1", false)
		return nil
	}
	t.Cleanup(func() { loginCommand = old })

	user, err := AddLogin("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(user, "b@example.com") {
		t.Fatalf("added %q", user)
	}
	if b := loginOf(t, "codex", "b@example.com"); !b.Current || !b.On {
		t.Fatalf("B should be current and on: %+v", b)
	}
	if a := loginOf(t, "codex", "a@example.com"); a.Current {
		t.Fatalf("A should no longer be current: %+v", a)
	}
	// the account the agent held before the login was kept
	stored, _ := storedLogin("codex", "a@example.com")
	if tokenOf(stored.Cred, "refresh_token") != "ra1" {
		t.Fatalf("A's sign-in was lost: %v", stored.Cred)
	}
}

func TestClaudeSwitchKeepsOtherConfig(t *testing.T) {
	home := claudeHome(t)
	claudeAs(t, home, "a@example.com", "sk-a", "ra1")
	Logins("claude")
	claudeAs(t, home, "b@example.com", "sk-b", "rb1")
	Logins("claude")
	if b := loginOf(t, "claude", "b@example.com"); !b.Current || !b.On {
		t.Fatalf("B should be current and on: %+v", b)
	}

	if err := SwitchLogin("claude", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	creds := readJSONFile(t, filepath.Join(home, ".claude", ".credentials.json"))
	oauth, _ := creds["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "sk-a" || creds["mcpOAuth"] == nil {
		t.Fatalf("credentials after switch: %v", creds)
	}
	state := readJSONFile(t, filepath.Join(home, ".claude.json"))
	oa, _ := state["oauthAccount"].(map[string]any)
	if oa["emailAddress"] != "a@example.com" || state["numStartups"] != float64(7) || state["projects"] == nil {
		t.Fatalf("state after switch: %v", state)
	}
	if a := loginOf(t, "claude", "a@example.com"); !a.Current {
		t.Fatalf("A should be current: %+v", a)
	}
	stored, _ := storedLogin("claude", "b@example.com")
	if ob, _ := stored.Cred["claudeAiOauth"].(map[string]any); ob["refreshToken"] != "rb1" {
		t.Fatalf("B's sign-in was not kept: %v", stored.Cred)
	}
}

func TestLoginUsageCachesAndKeepsLastKnown(t *testing.T) {
	home := codexHome(t)
	accessA, accessB := twoCodex(t, home)

	var hits atomic.Int32
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		hits.Add(1)
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == accessB && fail.Load() {
			w.WriteHeader(500)
			return
		}
		used := map[string]float64{accessA: 12, accessB: 5}[tok]
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": used, "limit_window_seconds": 18000},
			"secondary_window": map[string]any{"used_percent": used * 4, "limit_window_seconds": 604800},
		}})
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase; forgetLoginUsage() })
	forgetLoginUsage()

	ctx := context.Background()
	got := LoginUsage(ctx, "codex")
	a, b := got["a@example.com"], got["b@example.com"]
	if a.FiveHourUsed == nil || *a.FiveHourUsed != 12 || a.WeeklyUsed == nil || *a.WeeklyUsed != 48 {
		t.Fatalf("A: %+v", a)
	}
	if b.FiveHourUsed == nil || *b.FiveHourUsed != 5 || b.WeeklyUsed == nil || *b.WeeklyUsed != 20 {
		t.Fatalf("B: %+v", b)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("first call made %d requests", n)
	}

	// within a minute the answer is reused
	LoginUsage(ctx, "codex")
	if n := hits.Load(); n != 2 {
		t.Fatalf("a fresh answer was refetched (%d requests)", n)
	}

	// once it is stale, B's query fails: B keeps what it last showed
	loginUsageCache.Lock()
	e := loginUsageCache.by["codex"]
	e.at = time.Now().Add(-2 * time.Minute)
	loginUsageCache.by["codex"] = e
	loginUsageCache.Unlock()
	fail.Store(true)
	got = LoginUsage(ctx, "codex")
	if b := got["b@example.com"]; b.Error != "" || b.FiveHourUsed == nil || *b.FiveHourUsed != 5 {
		t.Fatalf("B after a failed query: %+v", b)
	}
	if a := got["a@example.com"]; a.FiveHourUsed == nil || *a.FiveHourUsed != 12 {
		t.Fatalf("A: %+v", a)
	}

	// an agent without subscription usage gives an empty map
	if m := LoginUsage(ctx, "copilot"); m == nil || len(m) != 0 {
		t.Fatalf("copilot usage: %v", m)
	}
}
