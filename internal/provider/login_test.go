package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// acctxSetup isolates a home and config directory, and points Claude Code at
// a temp directory with no Keychain and no CLI behind it.
func acctxSetup(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

// acctxCodex is a ChatGPT sign-in as Codex CLI writes it. The access token is
// unexpired, so nothing tries to refresh it. It returns the file and the token.
func acctxCodex(email, plan, refresh string) (map[string]any, string) {
	access := fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": email})
	auth := map[string]any{"auth_mode": "chatgpt", "last_refresh": "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan, "chatgpt_account_id": "acct-" + email}}),
			"access_token": access, "refresh_token": refresh, "account_id": "acct-" + email,
		}}
	return auth, access
}

func acctxRead(t *testing.T, path string) map[string]any {
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

func acctxRefreshToken(t *testing.T, auth map[string]any) string {
	t.Helper()
	toks, ok := auth["tokens"].(map[string]any)
	if !ok {
		t.Fatalf("no tokens in %v", auth)
	}
	s, _ := toks["refresh_token"].(string)
	return s
}

func acctxAuthPath(home string) string { return filepath.Join(home, ".codex", "auth.json") }

func acctxLogin(t *testing.T, ls []Login, user string) (Login, bool) {
	t.Helper()
	for _, l := range ls {
		if strings.EqualFold(l.User, user) {
			return l, true
		}
	}
	return Login{}, false
}

func TestAcctxCurrentIsRememberedNotCopied(t *testing.T) {
	home := acctxSetup(t)
	a, _ := acctxCodex("a@example.com", "pro", "ra")
	writeFile(t, acctxAuthPath(home), a)

	ls := Logins("codex")
	if len(ls) != 1 || !ls[0].Current || !ls[0].On || ls[0].User != "a@example.com" || ls[0].Plan != "pro" {
		t.Fatalf("logins = %+v", ls)
	}
	// the agent holds the current sign-in; magpie must not keep a second copy
	if fileExists(loginCreds("codex", "a@example.com")) {
		t.Fatal("the current sign-in was copied into magpie's store")
	}
}

func TestAcctxSwitchKeepsLatestCredentialsAndOtherFields(t *testing.T) {
	home := acctxSetup(t)
	a, _ := acctxCodex("a@example.com", "pro", "ra-rotated")
	a["note"] = "keep me"
	writeFile(t, acctxAuthPath(home), a)
	b, _ := acctxCodex("b@example.com", "plus", "rb")
	writeFile(t, loginCreds("codex", "b@example.com"), b)
	writeLogins([]loginRecord{
		{Agent: "codex", User: "a@example.com", Plan: "pro"},
		{Agent: "codex", User: "b@example.com", Plan: "plus", On: true},
	})

	if err := SwitchLogin("codex", "B@Example.com"); err != nil {
		t.Fatal(err)
	}
	cur := acctxRead(t, acctxAuthPath(home))
	if acctxRefreshToken(t, cur) != "rb" || cur["note"] != "keep me" {
		t.Fatalf("agent file after switch: %v", cur)
	}
	// the account switched away from keeps the tokens it had last
	if got := acctxRefreshToken(t, acctxRead(t, loginCreds("codex", "a@example.com"))); got != "ra-rotated" {
		t.Fatalf("saved refresh token = %q, want the latest", got)
	}
	if fileExists(loginCreds("codex", "b@example.com")) {
		t.Fatal("the account now signed in still has a copy in magpie's store")
	}
	ls := Logins("codex")
	if l, ok := acctxLogin(t, ls, "b@example.com"); !ok || !l.Current {
		t.Fatalf("b is not the current login: %+v", ls)
	}
	// the replaced account inherits the switched-on state of the one it gave way to
	if l, ok := acctxLogin(t, ls, "a@example.com"); !ok || l.Current || !l.On {
		t.Fatalf("a after switch: %+v", ls)
	}
	if p, err := Find("codex"); err != nil || p.Account == nil || p.Account.User != "b@example.com" {
		t.Fatalf("codex provider after switch: %+v %v", p, err)
	}
}

func TestAcctxCredentialFilesAreOwnerOnly(t *testing.T) {
	home := acctxSetup(t)
	a, _ := acctxCodex("a@example.com", "pro", "ra")
	writeFile(t, acctxAuthPath(home), a)
	b, _ := acctxCodex("b@example.com", "plus", "rb")
	writeFile(t, loginCreds("codex", "b@example.com"), b)
	writeLogins([]loginRecord{{Agent: "codex", User: "b@example.com", Plan: "plus"}})
	if err := SwitchLogin("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{loginCreds("codex", "a@example.com"), loginIndex(), acctxAuthPath(home)} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", filepath.Base(p), st.Mode().Perm())
		}
	}
}

func TestAcctxSwitchUnknownAccountFails(t *testing.T) {
	home := acctxSetup(t)
	a, _ := acctxCodex("a@example.com", "pro", "ra")
	writeFile(t, acctxAuthPath(home), a)
	if err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switching to an unknown account succeeded")
	}
	if got := acctxRefreshToken(t, acctxRead(t, acctxAuthPath(home))); got != "ra" {
		t.Fatalf("a failed switch changed the agent's sign-in: %q", got)
	}
}

func TestAcctxForgetAndSwitchOnRules(t *testing.T) {
	home := acctxSetup(t)
	a, _ := acctxCodex("a@example.com", "pro", "ra")
	writeFile(t, acctxAuthPath(home), a)
	for _, u := range []string{"b@example.com", "c@example.com"} {
		b, _ := acctxCodex(u, "plus", "r-"+u)
		writeFile(t, loginCreds("codex", u), b)
	}
	writeLogins([]loginRecord{
		{Agent: "codex", User: "a@example.com", Plan: "pro"},
		{Agent: "codex", User: "b@example.com", Plan: "plus"},
		{Agent: "codex", User: "c@example.com", Plan: "plus"},
	})

	if err := ForgetLogin("codex", "a@example.com"); err == nil {
		t.Fatal("forgot the account the agent is signed in as")
	}
	if err := SetLoginOn("codex", "a@example.com", false); err == nil {
		t.Fatal("switched off the current account")
	}
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatalf("switching on the current account: %v", err)
	}
	if err := SetLoginOn("codex", "b@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := ForgetLogin("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if fileExists(loginCreds("codex", "b@example.com")) {
		t.Fatal("forgetting an account left its credentials behind")
	}
	ls := Logins("codex")
	if _, ok := acctxLogin(t, ls, "b@example.com"); ok {
		t.Fatalf("forgotten account still listed: %+v", ls)
	}
	if err := SetLoginOn("codex", "b@example.com", true); err == nil {
		t.Fatal("switched on an account that was forgotten")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("switched on an unknown account")
	}
	if err := ForgetLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("forgot an unknown account")
	}
}

func TestAcctxServingOrderIsCurrentThenSaved(t *testing.T) {
	home := acctxSetup(t)
	a, _ := acctxCodex("a@example.com", "pro", "ra")
	writeFile(t, acctxAuthPath(home), a)
	recs := []loginRecord{{Agent: "codex", User: "a@example.com", Plan: "pro"}}
	for _, x := range []struct {
		user string
		on   bool
	}{{"b@example.com", true}, {"d@example.com", false}, {"c@example.com", true}} {
		s, _ := acctxCodex(x.user, "plus", "r-"+x.user)
		writeFile(t, loginCreds("codex", x.user), s)
		recs = append(recs, loginRecord{Agent: "codex", User: x.user, Plan: "plus", On: x.on})
	}
	writeLogins(recs)

	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range p.Credentials() {
		names = append(names, c.Name)
	}
	if want := []string{"a@example.com", "b@example.com", "c@example.com"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("serving order = %v, want %v", names, want)
	}
	// each candidate signs with its own tokens
	for _, c := range p.Credentials() {
		if c.Account == nil || c.Account.User != c.Name {
			t.Fatalf("candidate %q carries account %+v", c.Name, c.Account)
		}
	}
}

func TestAcctxClaudeSwitchPreservesOtherFields(t *testing.T) {
	acctxSetup(t)
	oldIdentity := claudeLoginIdentity
	claudeLoginIdentity = func() (string, string, bool) {
		c, _, ok := readClaudeCredential()
		if !ok {
			return "", "", true
		}
		switch c.OAuth.AccessToken {
		case "tok-a":
			return "a@example.com", "max", false
		case "tok-b":
			return "b@example.com", "pro", false
		}
		return "", "", true
	}
	t.Cleanup(func() { claudeLoginIdentity = oldIdentity; forgetClaudeCredential() })
	exp := time.Now().Add(time.Hour).UnixMilli()
	writeFile(t, filepath.Join(claudeConfigDir(), ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "tok-a", "refreshToken": "ref-a", "expiresAt": exp, "subscriptionType": "max"},
		"mcpOAuth":      map[string]any{"srv": map[string]any{"token": "m"}},
	})
	forgetClaudeCredential()
	writeFile(t, loginCreds("claude", "b@example.com"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "tok-b", "refreshToken": "ref-b", "expiresAt": exp, "subscriptionType": "pro"},
	})
	writeLogins([]loginRecord{
		{Agent: "claude", User: "a@example.com", Plan: "max"},
		{Agent: "claude", User: "b@example.com", Plan: "pro"},
	})

	if err := SwitchLogin("cc", "B@EXAMPLE.com"); err != nil {
		t.Fatal(err)
	}
	cur := acctxRead(t, filepath.Join(claudeConfigDir(), ".credentials.json"))
	oauth, _ := cur["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "tok-b" || oauth["refreshToken"] != "ref-b" {
		t.Fatalf("claude sign-in after switch: %v", oauth)
	}
	if _, ok := cur["mcpOAuth"]; !ok {
		t.Fatalf("switch dropped another field of the agent's file: %v", cur)
	}
	saved := acctxRead(t, loginCreds("claude", "a@example.com"))
	if so, _ := saved["claudeAiOauth"].(map[string]any); so["refreshToken"] != "ref-a" {
		t.Fatalf("replaced claude sign-in not kept: %v", saved)
	}
	if fileExists(loginCreds("claude", "b@example.com")) {
		t.Fatal("switched-in claude sign-in still stored by magpie")
	}
}

func TestAcctxLoginUsagePerAccount(t *testing.T) {
	home := acctxSetup(t)
	const tokA, tokB = "tok-usage-a", "tok-usage-b"
	var mu sync.Mutex
	failB := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/wham/usage" {
			http.NotFound(w, r)
			return
		}
		used := map[string][2]float64{tokA: {30, 60}, tokB: {10, 20}}
		u, ok := used[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok || (failB && u[0] == 10) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":` +
			jsonNum(u[0]) + `,"limit_window_seconds":18000,"reset_after_seconds":60},"secondary_window":{"used_percent":` +
			jsonNum(u[1]) + `,"limit_window_seconds":604800,"reset_after_seconds":60}}}`))
	}))
	t.Cleanup(srv.Close)
	oldBase := CodexBase
	CodexBase = srv.URL + "/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	resetUsage := func() {
		loginUsageCache.Lock()
		loginUsageCache.at, loginUsageCache.data = nil, nil
		loginUsageCache.Unlock()
	}
	resetUsage()
	t.Cleanup(resetUsage)

	a, _ := acctxCodex("a@example.com", "pro", "ra")
	a["tokens"].(map[string]any)["access_token"] = tokA
	writeFile(t, acctxAuthPath(home), a)
	b, _ := acctxCodex("b@example.com", "plus", "rb")
	b["tokens"].(map[string]any)["access_token"] = tokB
	writeFile(t, loginCreds("codex", "b@example.com"), b)
	writeLogins([]loginRecord{{Agent: "codex", User: "b@example.com", Plan: "plus"}})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := LoginUsage(ctx, "codex")
	check := func(user string, want [2]float64) {
		t.Helper()
		q, ok := got[user]
		if !ok || len(q.Windows) != 2 || q.Windows[0].Name != "5 hours" || q.Windows[0].Used != want[0] ||
			q.Windows[1].Name != "7 days" || q.Windows[1].Used != want[1] {
			t.Fatalf("usage for %s = %+v, want 5 hours %v / 7 days %v", user, q, want[0], want[1])
		}
	}
	check("a@example.com", [2]float64{30, 60})
	check("b@example.com", [2]float64{10, 20})

	// B fails now; its earlier reading stays. The cache is cleared to force a fetch.
	mu.Lock()
	failB = true
	mu.Unlock()
	loginUsageCache.Lock()
	loginUsageCache.at["codex"] = time.Time{}
	loginUsageCache.Unlock()
	got = LoginUsage(ctx, "codex")
	check("b@example.com", [2]float64{10, 20})
	if q := got["b@example.com"]; q.Error != "" {
		t.Fatalf("a failed refresh replaced the known reading: %+v", q)
	}

	if other := LoginUsage(ctx, "copilot"); other == nil || len(other) != 0 {
		t.Fatalf("unsupported agent gave %v, want an empty map", other)
	}
}

func jsonNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func TestAcctxAddLoginRemembersPreviousAndSwitchesNewOn(t *testing.T) {
	home := acctxSetup(t)
	a, _ := acctxCodex("a@example.com", "pro", "ra")
	writeFile(t, acctxAuthPath(home), a)

	ls, err := AddLogin("codex", func() error {
		b, _ := acctxCodex("b@example.com", "plus", "rb")
		writeFile(t, acctxAuthPath(home), b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if l, ok := acctxLogin(t, ls, "b@example.com"); !ok || !l.Current || !l.On {
		t.Fatalf("new account: %+v", ls)
	}
	if l, ok := acctxLogin(t, ls, "a@example.com"); !ok || l.Current {
		t.Fatalf("previous account: %+v", ls)
	}
	if got := acctxRefreshToken(t, acctxRead(t, loginCreds("codex", "a@example.com"))); got != "ra" {
		t.Fatalf("previous account's kept tokens = %q", got)
	}
}

func TestAcctxUnknownAgentIsRefused(t *testing.T) {
	acctxSetup(t)
	if _, ok := CanonicalAgent("gemini"); ok {
		t.Fatal("gemini accepted as an agent with accounts")
	}
	if c, ok := CanonicalAgent("CC"); !ok || c != "claude" {
		t.Fatalf("cc = %q, %v", c, ok)
	}
	if err := SwitchLogin("gemini", "x@example.com"); err == nil {
		t.Error("switch accepted an unknown agent")
	}
	if err := ForgetLogin("gemini", "x@example.com"); err == nil {
		t.Error("forget accepted an unknown agent")
	}
	if err := SetLoginOn("gemini", "x@example.com", true); err == nil {
		t.Error("on accepted an unknown agent")
	}
	if _, err := AddLogin("gemini", func() error { t.Fatal("ran a sign-in for an unknown agent"); return nil }); err == nil {
		t.Error("add accepted an unknown agent")
	}
	if got := Logins("gemini"); got != nil {
		t.Errorf("listed accounts of an unknown agent: %+v", got)
	}
}
