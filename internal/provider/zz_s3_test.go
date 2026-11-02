package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// zzCodexLoginAs makes Codex CLI sign in to an account: auth.json as the CLI
// would write it, with keys of its own that nobody else may touch.
func zzCodexLoginAs(t *testing.T, home, email, acctID, refresh string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"keep_me":        "yes",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{
				"chatgpt_plan_type": "plus", "chatgpt_account_id": acctID}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": email}),
			"refresh_token": refresh, "account_id": acctID,
		},
	})
}

func zzReadCodexAuth(t *testing.T, home string) map[string]any {
	t.Helper()
	m, err := readRawJSON(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func zzCodexRefreshToken(t *testing.T, home string) string {
	t.Helper()
	toks, _ := zzReadCodexAuth(t, home)["tokens"].(map[string]any)
	s, _ := toks["refresh_token"].(string)
	return s
}

func zzLoginUsers(t *testing.T, agent string) string {
	t.Helper()
	ls, err := Logins(agent)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range ls {
		s := l.User
		if l.Current {
			s += "*"
		}
		if l.On && !l.Current {
			s += "+"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestZZCodexAccountsRememberSwitchForget(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	zzCodexLoginAs(t, home, "a@example.com", "acct-a", "r-a")
	if got := zzLoginUsers(t, "codex"); got != "a@example.com*" {
		t.Fatalf("logins = %q", got)
	}
	// signing in to another account in the agent: both are remembered
	zzCodexLoginAs(t, home, "b@example.com", "acct-b", "r-b")
	if got := zzLoginUsers(t, "codex"); got != "a@example.com,b@example.com*" {
		t.Fatalf("logins = %q", got)
	}
	st, err := os.Stat(loginsPath())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("accounts.json: %v %v", st, err)
	}

	// the agent refreshes B's tokens before the switch: the new ones are kept
	zzCodexLoginAs(t, home, "b@example.com", "acct-b", "r-b2")
	l, err := SwitchLogin("codex", "A@Example.COM")
	if err != nil || l.User != "a@example.com" || !l.Current {
		t.Fatalf("switch: %+v %v", l, err)
	}
	auth := zzReadCodexAuth(t, home)
	if zzCodexRefreshToken(t, home) != "r-a" || auth["keep_me"] != "yes" || auth["auth_mode"] != "chatgpt" {
		t.Fatalf("auth.json after switch: %v", auth)
	}
	if _, ok := auth["OPENAI_API_KEY"]; !ok {
		t.Fatalf("lost a field of auth.json: %v", auth)
	}
	if got := zzLoginUsers(t, "codex"); got != "a@example.com*,b@example.com" {
		t.Fatalf("logins = %q", got)
	}
	// ... and back: B has its latest tokens again
	if _, err := SwitchLogin("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if zzCodexRefreshToken(t, home) != "r-b2" {
		t.Fatalf("B's latest tokens lost: %v", zzReadCodexAuth(t, home))
	}

	if _, err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Error("switched to an unknown account")
	}
	if err := ForgetLogin("codex", "B@example.com"); err == nil || !strings.Contains(err.Error(), "signed in") {
		t.Errorf("forgot the signed-in account: %v", err)
	}
	if err := ForgetLogin("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := zzLoginUsers(t, "codex"); got != "b@example.com*" {
		t.Fatalf("logins = %q", got)
	}
	if err := ForgetLogin("codex", "a@example.com"); err == nil {
		t.Error("forgot an account twice")
	}
	if _, err := Logins("gemini"); err == nil {
		t.Error("unknown agent listed")
	}
}

func zzClaudeLoginAs(t *testing.T, home, email, access, refresh string) {
	t.Helper()
	path := filepath.Join(home, ".claude", ".credentials.json")
	raw, err := readRawJSON(path)
	if err != nil {
		raw = map[string]any{"mcpOAuth": map[string]any{"plugin:slack": map[string]any{"serverName": "Slack"}}}
	}
	raw["claudeAiOauth"] = map[string]any{"accessToken": access, "refreshToken": refresh,
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max"}
	writeFile(t, path, raw)
	cfg, err := readRawJSON(filepath.Join(home, ".claude.json"))
	if err != nil {
		cfg = map[string]any{"numStartups": 42, "projects": map[string]any{"/x": map[string]any{"a": 1}}}
	}
	cfg["oauthAccount"] = map[string]any{"emailAddress": email, "organizationName": email + "'s org"}
	writeFile(t, filepath.Join(home, ".claude.json"), cfg)
	forgetClaudeCredential()
	forgetClaudeStatus()
}

func TestZZClaudeAccountsSwitchKeepsTheRest(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	zzClaudeLoginAs(t, home, "one@example.com", "at-1", "rt-1")
	if got := zzLoginUsers(t, "cc"); got != "one@example.com*" {
		t.Fatalf("logins = %q", got)
	}
	zzClaudeLoginAs(t, home, "two@example.com", "at-2", "rt-2")
	if got := zzLoginUsers(t, "claude"); got != "one@example.com,two@example.com*" {
		t.Fatalf("logins = %q", got)
	}
	// Claude Code rotates two's token before the switch
	zzClaudeLoginAs(t, home, "two@example.com", "at-2b", "rt-2b")
	l, err := SwitchLogin("claude", "ONE@example.com")
	if err != nil || !l.Current {
		t.Fatalf("switch: %+v %v", l, err)
	}
	creds, _ := readRawJSON(filepath.Join(home, ".claude", ".credentials.json"))
	oauth, _ := creds["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "at-1" || oauth["refreshToken"] != "rt-1" || creds["mcpOAuth"] == nil {
		t.Fatalf("credentials: %v", creds)
	}
	cfg, _ := readRawJSON(filepath.Join(home, ".claude.json"))
	acct, _ := cfg["oauthAccount"].(map[string]any)
	if acct["emailAddress"] != "one@example.com" || cfg["numStartups"] == nil || cfg["projects"] == nil {
		t.Fatalf(".claude.json: %v", cfg)
	}
	if _, err := SwitchLogin("claude", "two@example.com"); err != nil {
		t.Fatal(err)
	}
	creds, _ = readRawJSON(filepath.Join(home, ".claude", ".credentials.json"))
	oauth, _ = creds["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "at-2b" || oauth["refreshToken"] != "rt-2b" {
		t.Fatalf("two's latest tokens lost: %v", creds)
	}
	if err := ForgetLogin("claude", "two@example.com"); err == nil {
		t.Error("forgot the signed-in account")
	}
}

func TestZZSetLoginOnAndSwitchInheritsOn(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	zzCodexLoginAs(t, home, "a@example.com", "acct-a", "r-a")
	zzLoginUsers(t, "codex")
	zzCodexLoginAs(t, home, "b@example.com", "acct-b", "r-b")
	zzLoginUsers(t, "codex")

	if err := SetLoginOn("codex", "b@example.com", false); err == nil {
		t.Error("switched off the signed-in account")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Error("switched on an unknown account")
	}
	if err := SetLoginOn("codex", "A@example.com", true); err != nil {
		t.Fatal(err)
	}
	if got := zzLoginUsers(t, "codex"); got != "a@example.com+,b@example.com*" {
		t.Fatalf("logins = %q", got)
	}
	// a switch: the one leaving takes over the state the one arriving had
	if _, err := SwitchLogin("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := zzLoginUsers(t, "codex"); got != "a@example.com*,b@example.com+" {
		t.Fatalf("logins = %q", got)
	}
	if err := SetLoginOn("codex", "b@example.com", false); err != nil {
		t.Fatal(err)
	}
	if _, err := SwitchLogin("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := zzLoginUsers(t, "codex"); got != "a@example.com,b@example.com*" {
		t.Fatalf("logins = %q", got)
	}
	if err := SetLoginOn("gemini", "x", true); err == nil {
		t.Error("unknown agent")
	}
}

func TestZZOtherLoginsSignWithTheirOwnTokens(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	zzCodexLoginAs(t, home, "a@example.com", "acct-a", "r-a")
	zzLoginUsers(t, "codex")
	zzCodexLoginAs(t, home, "b@example.com", "acct-b", "r-b")
	zzLoginUsers(t, "codex")
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.OtherLogins(); len(got) != 0 {
		t.Fatalf("others while all are off: %d", len(got))
	}
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	others := p.OtherLogins()
	if len(others) != 1 || others[0].Account.User != "a@example.com" || !others[0].Account.Stored {
		t.Fatalf("others: %+v", others)
	}
	req, _ := http.NewRequest("POST", "http://x/responses", nil)
	if err := others[0].Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("chatgpt-account-id") != "acct-a" {
		t.Fatalf("signed as %v", req.Header)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "Bearer h.") {
		t.Fatalf("auth %q", req.Header.Get("Authorization"))
	}
	// the agent's own store was not touched
	if zzCodexRefreshToken(t, home) != "r-b" {
		t.Fatal("agent's auth.json changed")
	}
}

func TestZZStoredCodexTokenRefreshStaysInMagpie(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	// A's access token is about to expire
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      fakeJWT(map[string]any{"email": "a@example.com"}),
		"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Minute).Unix())}),
		"refresh_token": "r-a", "account_id": "acct-a"}})
	zzLoginUsers(t, "codex")
	zzCodexLoginAs(t, home, "b@example.com", "acct-b", "r-b")
	zzLoginUsers(t, "codex")
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	fresh := fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "n": "fresh"})
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		sent = b["refresh_token"]
		json.NewEncoder(w).Encode(map[string]string{"access_token": fresh, "refresh_token": "r-a2"})
	}))
	defer srv.Close()
	oldURL := codexTokenURL
	codexTokenURL = srv.URL
	defer func() { codexTokenURL = oldURL }()

	tok, id, err := loginToken(context.Background(), "codex", "a@example.com")
	if err != nil || tok != fresh || id != "acct-a" || sent != "r-a" {
		t.Fatalf("token %q %q %v (sent %q)", tok, id, err, sent)
	}
	if zzCodexRefreshToken(t, home) != "r-b" {
		t.Fatal("refresh wrote to the agent's auth.json")
	}
	// the rotated refresh token is in magpie's store: switching hands it over
	if _, err := SwitchLogin("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if zzCodexRefreshToken(t, home) != "r-a2" {
		t.Fatalf("rotated token lost: %v", zzReadCodexAuth(t, home))
	}
}

func TestZZAddLoginKeepsTheCurrentAccountAndSwitchesTheNewOneOn(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	zzCodexLoginAs(t, home, "a@example.com", "acct-a", "r-a")
	old := loginRunner
	defer func() { loginRunner = old }()
	loginRunner = func(ctx context.Context, agent string) error {
		if agent != "codex" {
			t.Errorf("agent %q", agent)
		}
		zzCodexLoginAs(t, home, "b@example.com", "acct-b", "r-b")
		return nil
	}
	l, err := AddLogin(context.Background(), "codex")
	if err != nil || l.User != "b@example.com" || l.Current || !l.On {
		t.Fatalf("add: %+v %v", l, err)
	}
	// the agent is back on the account it was on
	if got := zzLoginUsers(t, "codex"); got != "a@example.com*,b@example.com+" {
		t.Fatalf("logins = %q", got)
	}
	if zzCodexRefreshToken(t, home) != "r-a" {
		t.Fatal("agent not put back on the first account")
	}
	loginRunner = func(ctx context.Context, agent string) error { return os.ErrPermission }
	if _, err := AddLogin(context.Background(), "codex"); err == nil {
		t.Error("a failed sign-in was taken for success")
	}
}

func TestZZLoginUsagePerAccount(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	zzCodexLoginAs(t, home, "a@example.com", "acct-a", "r-a")
	zzLoginUsers(t, "codex")
	zzCodexLoginAs(t, home, "b@example.com", "acct-b", "r-b")
	zzLoginUsers(t, "codex")

	failB := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			http.NotFound(w, r)
			return
		}
		switch r.Header.Get("chatgpt-account-id") {
		case "acct-a":
			w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":12,"limit_window_seconds":18000},"secondary_window":{"used_percent":34,"limit_window_seconds":604800}}}`))
		case "acct-b":
			if failB {
				http.Error(w, "no", 500)
				return
			}
			w.Write([]byte(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":56,"limit_window_seconds":18000},"secondary_window":{"used_percent":78,"limit_window_seconds":604800}}}`))
		}
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	defer func() { CodexBase = oldBase }()

	got := LoginUsage(context.Background(), "codex")
	a, b := got["a@example.com"], got["b@example.com"]
	if len(got) != 2 || len(a.Windows) != 2 || a.Windows[0].Name != "5 hours" || a.Windows[0].Used != 12 || a.Windows[1].Used != 34 {
		t.Fatalf("a: %+v (all %+v)", a, got)
	}
	if len(b.Windows) != 2 || b.Windows[0].Used != 56 || b.Windows[1].Name != "7 days" || b.Windows[1].Used != 78 {
		t.Fatalf("b: %+v", b)
	}

	// a failing account keeps what was known of it, once the minute is up
	failB = true
	c := &loginUsageCache
	c.Lock()
	c.at["codex"] = time.Now().Add(-2 * time.Minute)
	c.Unlock()
	got = LoginUsage(context.Background(), "codex")
	if b := got["b@example.com"]; b.Error != "" || len(b.Windows) != 2 || b.Windows[0].Used != 56 {
		t.Fatalf("b after failure: %+v", b)
	}
	if g := LoginUsage(context.Background(), "gemini"); g == nil || len(g) != 0 {
		t.Error("unsupported agent should give an empty map")
	}
}

func TestZZLoginUsageReusedForAMinute(t *testing.T) {
	home := claudeHome(t)
	forgetSync()
	zzCodexLoginAs(t, home, "a@example.com", "acct-a", "r-a")
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000}}}`))
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	defer func() { CodexBase = oldBase }()
	LoginUsage(context.Background(), "codex")
	LoginUsage(context.Background(), "codex")
	if n != 1 {
		t.Fatalf("asked %d times", n)
	}
}
