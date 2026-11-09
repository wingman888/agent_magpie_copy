package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// magS3Env gives a test its own home, config and Claude directory, and keeps the
// real claude command out of it.
func magS3Env(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	oldExe := claudeExecutable
	claudeExecutable = func() string { return "" }
	magS3ResetCaches()
	t.Cleanup(func() {
		claudeExecutable = oldExe
		magS3ResetCaches()
	})
	return dir
}

// magS3ResetCaches drops what the package remembers between tests.
func magS3ResetCaches() {
	forgetClaudeCredential()
	forgetClaudeStatus()
	magObserveClock.Lock()
	magObserveClock.at = time.Time{}
	magObserveClock.Unlock()
	magLoginUsageCache.Lock()
	magLoginUsageCache.by = nil
	magLoginUsageCache.Unlock()
}

// magS3JWT builds an unsigned JWT with the given claims.
func magS3JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// magS3Access is an access token named name that never needs refreshing.
func magS3Access(name string) string {
	return magS3JWT(map[string]any{"name": name, "exp": int64(4102444800)}) // 2100-01-01
}

// magS3CodexAuth writes Codex CLI's auth file as if signed in as email.
func magS3CodexAuth(t *testing.T, email, name string) {
	t.Helper()
	path := magCodexAuthPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": magS3JWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  magS3Access(name),
			"refresh_token": "refresh-" + name,
			"account_id":    "acct-" + name,
		},
		"last_refresh": "2026-01-01T00:00:00Z",
	}
	b, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// magS3AuthName is the name of the access token in Codex CLI's auth file.
func magS3AuthName(t *testing.T) string {
	t.Helper()
	var a codexAuth
	if !readJSON(magCodexAuthPath(), &a) {
		t.Fatal("no Codex auth file")
	}
	name, _ := jwtClaims(a.Tokens.AccessToken)["name"].(string)
	return name
}

// magS3StashName is the name of the access token magpie keeps for a remembered
// Codex account.
func magS3StashName(t *testing.T, user string) string {
	t.Helper()
	m, err := magReadStash("codex", user)
	if err != nil {
		t.Fatal(err)
	}
	tokens, _ := m["tokens"].(map[string]any)
	name, _ := jwtClaims(fmt.Sprint(tokens["access_token"]))["name"].(string)
	return name
}

// magS3KeepSignIn saves the sign-in agent holds now as user's, the copy a switch
// (or an add) makes when it hands the agent over, so user can be switched back
// to. Tests that cannot run the agent's own sign-in use it in its place.
func magS3KeepSignIn(t *testing.T, agent, user string) {
	t.Helper()
	login, ok := magAgentLogin(agent)
	if !ok {
		t.Fatalf("no %s sign-in to keep", agent)
	}
	if _, err := magWriteStash(agent, user, login); err != nil {
		t.Fatal(err)
	}
	f := magLoadAccounts()
	i := magFindRemembered(f.Accounts, agent, user)
	if i < 0 {
		t.Fatalf("%s is not remembered", user)
	}
	f.Accounts[i].Saved = true
	if err := magStoreAccounts(f); err != nil {
		t.Fatal(err)
	}
}

// magS3TwoAccounts remembers a@ first, then signs in as b@, so b@ is current and
// a@ is remembered with its own saved sign-in.
func magS3TwoAccounts(t *testing.T) {
	t.Helper()
	magS3CodexAuth(t, "a@example.com", "a")
	if err := magObserveSignIns(true); err != nil {
		t.Fatal(err)
	}
	magS3KeepSignIn(t, "codex", "a@example.com")
	magS3CodexAuth(t, "b@example.com", "b")
	if err := magObserveSignIns(true); err != nil {
		t.Fatal(err)
	}
}

// magS3Current is the signed-in provider of agent.
func magS3Current(t *testing.T, agent string) Provider {
	t.Helper()
	for _, p := range Accounts() {
		if p.Account != nil && p.Account.Agent == agent {
			return p
		}
	}
	t.Fatalf("no %s sign-in", agent)
	return Provider{}
}

func TestS3SwitchKeepsTheRestAndSavesTheOutgoing(t *testing.T) {
	magS3Env(t)
	magS3TwoAccounts(t)
	// a key the switch must not touch: the rest of Codex's own file
	var raw map[string]any
	readJSON(magCodexAuthPath(), &raw)
	raw["custom"] = "keep"
	b, _ := json.MarshalIndent(raw, "", "  ")
	os.WriteFile(magCodexAuthPath(), b, 0o600)
	cfg := filepath.Join(filepath.Dir(magCodexAuthPath()), "config.toml")
	if err := os.WriteFile(cfg, []byte("model = \"gpt-5.5\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	got, err := SwitchAccount("codex", "A@Example.com")
	if err != nil || got != "a@example.com" {
		t.Fatalf("SwitchAccount = %q, %v", got, err)
	}
	if n := magS3AuthName(t); n != "a" {
		t.Fatalf("agent is signed in with token %q, want a", n)
	}
	var after map[string]any
	readJSON(magCodexAuthPath(), &after)
	if after["custom"] != "keep" {
		t.Fatalf("a key outside the sign-in changed: %v", after["custom"])
	}
	if b, _ := os.ReadFile(cfg); string(b) != "model = \"gpt-5.5\"\n" {
		t.Fatalf("config.toml changed: %q", b)
	}
	if n := magS3StashName(t, "b@example.com"); n != "b" {
		t.Fatalf("the account signed out of kept token %q, want its newest, b", n)
	}
	rows := Remembered("codex")
	if len(rows) != 2 {
		t.Fatalf("remembered %d accounts, want 2: %+v", len(rows), rows)
	}
	for _, r := range rows {
		switch {
		case strings.EqualFold(r.User, "a@example.com") && !r.Current:
			t.Fatalf("a should be the signed-in account: %+v", r)
		case strings.EqualFold(r.User, "b@example.com") && (r.Current || !r.Saved || !r.On):
			t.Fatalf("b should be remembered, saved, and on (it took a's on state): %+v", r)
		}
	}
}

func TestS3ForgetRefusesTheSignedInAccount(t *testing.T) {
	magS3Env(t)
	magS3TwoAccounts(t)
	if err := ForgetAccount("codex", "b@example.com"); err == nil {
		t.Fatal("forgot the account that is signed in")
	}
	if err := ForgetAccount("codex", "nobody@example.com"); err == nil {
		t.Fatal("forgot an account magpie never saw")
	}
	if err := ForgetAccount("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(magStashPath("codex", "a@example.com")); !os.IsNotExist(err) {
		t.Fatalf("the saved sign-in survived forgetting: %v", err)
	}
	rows := Remembered("codex")
	if len(rows) != 1 || rows[0].User != "b@example.com" || !rows[0].Current {
		t.Fatalf("after forgetting a: %+v", rows)
	}
}

func TestS3OnAccountServesWithItsOwnSignIn(t *testing.T) {
	magS3Env(t)
	magS3TwoAccounts(t)
	if err := SetLoginOn("codex", "b@example.com", false); err == nil {
		t.Fatal("the signed-in account was turned off")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("an unknown account was turned on")
	}
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	p := magS3Current(t, "codex")
	plans := p.KeyPlans(Responses, "gpt-5.5")
	if len(plans) != 2 {
		t.Fatalf("plans = %d, want the signed-in account then the on account", len(plans))
	}
	if plans[0].Provider.Account.User != "b@example.com" || plans[1].Provider.Account.User != "a@example.com" {
		t.Fatalf("order = %q, %q", plans[0].Provider.Account.User, plans[1].Provider.Account.User)
	}
	if plans[0].Ref == "" || plans[0].Ref == plans[1].Ref {
		t.Fatalf("accounts need distinct refs for cooling: %q, %q", plans[0].Ref, plans[1].Ref)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1/unused", nil)
	if err := plans[1].Provider.Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+magS3Access("a") {
		t.Fatalf("on account signed with %q, want its own token", got)
	}
	if n := magS3AuthName(t); n != "b" {
		t.Fatalf("Codex's own file now holds token %q; the on account must not write there", n)
	}
}

func TestS3LoginUsagePerAccountAndKeepsLastKnown(t *testing.T) {
	magS3Env(t)
	magS3TwoAccounts(t)

	var mu sync.Mutex
	hits := 0
	failB := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		name, _ := jwtClaims(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))["name"].(string)
		if name == "b" && failB {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		pct := 40
		if name == "b" {
			pct = 75
		}
		fmt.Fprintf(w, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":%d,"limit_window_seconds":18000,"reset_after_seconds":60},"secondary_window":{"used_percent":%d,"limit_window_seconds":604800,"reset_after_seconds":600}}}`, pct, pct+5)
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })

	ctx := context.Background()
	got := LoginUsage(ctx, "codex")
	if len(got) != 2 {
		t.Fatalf("usage for %d accounts, want 2: %+v", len(got), got)
	}
	if u := got["b@example.com"]; len(u.Windows) != 2 || u.Windows[0].Used != 75 || u.Windows[1].Used != 80 {
		t.Fatalf("b usage = %+v", u)
	}
	if u := got["a@example.com"]; len(u.Windows) != 2 || u.Windows[0].Used != 40 {
		t.Fatalf("a usage = %+v", u)
	}

	LoginUsage(ctx, "codex")
	mu.Lock()
	if hits != 2 {
		t.Fatalf("a second call within a minute asked the vendor again: %d requests", hits)
	}
	failB = true
	mu.Unlock()

	magLoginUsageCache.Lock()
	for _, e := range magLoginUsageCache.by {
		e.at = time.Time{}
	}
	magLoginUsageCache.Unlock()

	again := LoginUsage(ctx, "codex")
	if u := again["b@example.com"]; u.Error != "" || len(u.Windows) != 2 || u.Windows[0].Used != 75 {
		t.Fatalf("a failed query should keep b's last known usage: %+v", u)
	}
	if u := again["a@example.com"]; len(u.Windows) != 2 || u.Windows[0].Used != 40 {
		t.Fatalf("a usage = %+v", u)
	}
	if n := len(LoginUsage(ctx, "copilot")); n != 0 {
		t.Fatalf("an agent without accounts gave %d entries", n)
	}
}

func TestS3UnknownAgentsAreRefused(t *testing.T) {
	magS3Env(t)
	if err := SetLoginOn("copilot", "x@example.com", true); err == nil {
		t.Fatal("SetLoginOn accepted copilot")
	}
	if _, err := SwitchAccount("gemini", "x@example.com"); err == nil {
		t.Fatal("SwitchAccount accepted gemini")
	}
	if err := ForgetAccount("gemini", "x@example.com"); err == nil {
		t.Fatal("ForgetAccount accepted gemini")
	}
	if _, err := AddAccount("gemini"); err == nil {
		t.Fatal("AddAccount accepted gemini")
	}
	if n := len(Remembered("gemini")); n != 0 {
		t.Fatalf("gemini has %d remembered accounts", n)
	}
}

func TestS3SavedSignInsArePrivate(t *testing.T) {
	magS3Env(t)
	magS3TwoAccounts(t)
	for _, p := range []string{magAccountsIndex(), magStashPath("codex", "a@example.com")} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s is %v, want 0600", p, st.Mode().Perm())
		}
	}
}

func TestS3ClaudeSwitchKeepsOtherCredentialKeys(t *testing.T) {
	dir := magS3Env(t)
	claudeDir := filepath.Join(dir, "claude")
	who := filepath.Join(dir, "who.json")
	creds := filepath.Join(claudeDir, ".credentials.json")
	// a stand-in for `claude auth status --json`, which reports whoever the
	// test last wrote to who.json, as the real one does for the active login
	fake := filepath.Join(dir, "claude-fake")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat \""+who+"\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeExecutable = func() string { return fake }
	signIn := func(email, token string) {
		t.Helper()
		if err := os.MkdirAll(claudeDir, 0o700); err != nil {
			t.Fatal(err)
		}
		status := fmt.Sprintf(`{"loggedIn":true,"email":%q,"subscriptionType":"max"}`, email)
		if err := os.WriteFile(who, []byte(status), 0o644); err != nil {
			t.Fatal(err)
		}
		blob := map[string]any{
			"claudeAiOauth": map[string]any{"accessToken": token, "refreshToken": "r-" + token,
				"expiresAt": time.Now().Add(24 * time.Hour).UnixMilli(), "subscriptionType": "max"},
			"mcpOAuth": map[string]any{"server": token},
		}
		b, _ := json.Marshal(blob)
		if err := os.WriteFile(creds, b, 0o600); err != nil {
			t.Fatal(err)
		}
		magS3ResetCaches()
		claudeExecutable = func() string { return fake }
	}
	signIn("c1@example.com", "tok-c1")
	if err := magObserveSignIns(true); err != nil {
		t.Fatal(err)
	}
	magS3KeepSignIn(t, "claude", "c1@example.com")
	signIn("c2@example.com", "tok-c2")
	if err := magObserveSignIns(true); err != nil {
		t.Fatal(err)
	}

	got, err := SwitchAccount("claude", "C1@example.com")
	if err != nil || got != "c1@example.com" {
		t.Fatalf("SwitchAccount = %q, %v", got, err)
	}
	var raw map[string]any
	if b, err := os.ReadFile(creds); err != nil || json.Unmarshal(b, &raw) != nil {
		t.Fatalf("credentials unreadable: %v", err)
	}
	oauth, _ := raw["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "tok-c1" {
		t.Fatalf("claude signed in with %v, want tok-c1", oauth["accessToken"])
	}
	if mcp, _ := raw["mcpOAuth"].(map[string]any); mcp["server"] != "tok-c2" {
		t.Fatalf("a key outside the sign-in changed: %v", raw["mcpOAuth"])
	}
	saved, err := magReadStash("claude", "c2@example.com")
	if err != nil {
		t.Fatal(err)
	}
	savedOAuth, _ := saved["claudeAiOauth"].(map[string]any)
	if savedOAuth["accessToken"] != "tok-c2" {
		t.Fatalf("the account signed out of kept %v, want tok-c2", savedOAuth["accessToken"])
	}
}
