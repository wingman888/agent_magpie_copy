package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// codexLogin writes a ChatGPT login for email into the home's auth.json,
// keeping a foreign field Codex CLI owns.
func codexLogin(t *testing.T, home, email, refresh string, exp time.Time) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"custom":         map[string]any{"keep": "me"},
		"last_refresh":   "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{
				"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + email}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(exp.Unix()), "sub": email}),
			"refresh_token": refresh, "account_id": "acct-" + email,
		},
	})
}

func readAuth(t *testing.T, home string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := decodeMap(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func authRefresh(m map[string]any) string {
	s, _ := m["tokens"].(map[string]any)["refresh_token"].(string)
	return s
}

func TestCodexLoginsSwitchForget(t *testing.T) {
	home := claudeHome(t)
	codexLogin(t, home, "a@example.com", "ra", time.Now().Add(time.Hour))
	logins, err := Logins("codex")
	if err != nil || len(logins) != 1 || !logins[0].Current || logins[0].User != "a@example.com" || logins[0].Plan != "plus" {
		t.Fatalf("logins: %+v %v", logins, err)
	}
	// the agent signs in to another account behind magpie's back
	codexLogin(t, home, "b@example.com", "rb", time.Now().Add(time.Hour))
	logins, _ = Logins("cc2")
	if logins != nil {
		t.Fatal("unknown agent listed")
	}
	logins, _ = Logins("codex")
	if len(logins) != 2 || logins[0].Current || !logins[1].Current || !logins[0].Saved {
		t.Fatalf("logins: %+v", logins)
	}
	if st, err := os.Stat(LoginsPath()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("store: %v %v", st, err)
	}

	// B refreshes its token; the switch must keep that latest pair
	codexLogin(t, home, "b@example.com", "rb-latest", time.Now().Add(time.Hour))
	if _, err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switched to an unknown account")
	}
	l, err := SwitchLogin("codex", "A@Example.com")
	if err != nil || l.User != "a@example.com" {
		t.Fatalf("switch: %+v %v", l, err)
	}
	auth := readAuth(t, home)
	if authRefresh(auth) != "ra" || auth["custom"].(map[string]any)["keep"] != "me" || auth["auth_mode"] != "chatgpt" {
		t.Fatalf("auth after switch: %v", auth)
	}
	if _, ok := auth["OPENAI_API_KEY"]; !ok {
		t.Fatal("foreign field dropped")
	}
	e, _ := loginEntry("codex", "b@example.com")
	if e.Cred["refresh_token"] != "rb-latest" {
		t.Fatalf("outgoing credentials lost: %v", e.Cred)
	}
	logins, _ = Logins("codex")
	if !logins[0].Current || logins[1].Current {
		t.Fatalf("current: %+v", logins)
	}

	if err := ForgetLogin("codex", "a@example.com"); err == nil {
		t.Fatal("forgot the signed-in account")
	}
	if err := ForgetLogin("codex", "zed@example.com"); err == nil {
		t.Fatal("forgot an unknown account")
	}
	if err := ForgetLogin("codex", "B@example.com"); err != nil {
		t.Fatal(err)
	}
	if logins, _ = Logins("codex"); len(logins) != 1 {
		t.Fatalf("after forget: %+v", logins)
	}
	if _, err := Logins("gemini"); err == nil {
		t.Fatal("gemini has no accounts")
	}
	if a, err := LoginAgent("CC"); err != nil || a != "claude" {
		t.Fatalf("cc: %q %v", a, err)
	}
}

func TestLoginOnAndSwitchInheritance(t *testing.T) {
	home := claudeHome(t)
	codexLogin(t, home, "a@example.com", "ra", time.Now().Add(time.Hour))
	Logins("codex")
	codexLogin(t, home, "b@example.com", "rb", time.Now().Add(time.Hour))
	Logins("codex")
	codexLogin(t, home, "c@example.com", "rc", time.Now().Add(time.Hour))
	Logins("codex")

	if err := SetLoginOn("codex", "c@example.com", false); err == nil {
		t.Fatal("turned the current account off")
	}
	if err := SetLoginOn("codex", "c@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("unknown account")
	}
	if err := SetLoginOn("nope", "a@example.com", true); err == nil {
		t.Fatal("unknown agent")
	}
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}
	on := func() map[string]bool {
		m := map[string]bool{}
		ls, _ := Logins("codex")
		for _, l := range ls {
			m[l.User] = l.On
		}
		return m
	}
	if got := on(); !got["a@example.com"] || got["b@example.com"] || !got["c@example.com"] {
		t.Fatalf("on: %v", got)
	}
	// switching to A (on): C steps down and inherits A's state; to B (off): A would
	if _, err := SwitchLogin("codex", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := on(); !got["a@example.com"] || got["b@example.com"] || !got["c@example.com"] {
		t.Fatalf("after switch to a: %v", got)
	}
	if _, err := SwitchLogin("codex", "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := on(); got["a@example.com"] || !got["b@example.com"] {
		t.Fatalf("after switch to b: %v", got)
	}
}

// An account that is on alongside the current one signs with its own tokens,
// refreshes into magpie's store, and never touches the agent's credentials.
func TestLoginProvidersSignWithOwnCredentials(t *testing.T) {
	home := claudeHome(t)
	// A is saved with an already-expired token, then B takes over the agent
	codexLogin(t, home, "a@example.com", "ra", time.Now().Add(-time.Hour))
	Logins("codex")
	codexLogin(t, home, "b@example.com", "rb", time.Now().Add(time.Hour))
	if err := SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(b), `"ra"`) {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": "a-new"}),
			"refresh_token": "ra-2"})
	}))
	defer tokens.Close()
	old := codexTokenURL
	codexTokenURL = tokens.URL
	defer func() { codexTokenURL = old }()

	before, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	cur, ok := find(All(), "codex")
	if !ok || cur.Account.User != "b@example.com" {
		t.Fatalf("current: %+v", cur)
	}
	ps := LoginProviders(cur)
	if len(ps) != 2 || ps[0].Account.User != "b@example.com" || ps[1].Account.User != "a@example.com" {
		t.Fatalf("candidates: %+v", ps)
	}
	req, _ := http.NewRequest("POST", "http://x/responses", nil)
	if err := ps[1].Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.Header.Get("Authorization"), "Bearer h.") || req.Header.Get("chatgpt-account-id") != "acct-a@example.com" {
		t.Fatalf("headers: %v", req.Header)
	}
	if tok := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "); jwtClaims(tok)["sub"] != "a-new" {
		t.Fatalf("token not refreshed: %v", jwtClaims(tok))
	}
	after, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if string(before) != string(after) {
		t.Fatal("agent credentials written by a side account")
	}
	if e, _ := loginEntry("codex", "a@example.com"); e.Cred["refresh_token"] != "ra-2" {
		t.Fatalf("rotated refresh token not kept: %v", e.Cred)
	}
	// the current account's own provider still signs from the agent's file
	req, _ = http.NewRequest("POST", "http://x/responses", nil)
	if err := ps[0].Sign(context.Background(), req, Responses, nil); err != nil || req.Header.Get("chatgpt-account-id") != "acct-b@example.com" {
		t.Fatalf("current sign: %v %v", err, req.Header)
	}
	// turned off, it is no longer a candidate
	SetLoginOn("codex", "a@example.com", false)
	if ps = LoginProviders(cur); len(ps) != 1 {
		t.Fatalf("candidates after off: %d", len(ps))
	}
}

func TestClaudeLoginSwitch(t *testing.T) {
	home := claudeHome(t)
	creds := claudeSignIn(t, home, time.Now().Add(time.Hour))
	cfg := filepath.Join(home, ".claude.json")
	writeFile(t, cfg, map[string]any{"numStartups": 1234567890123, "theme": "dark",
		"oauthAccount": map[string]any{"emailAddress": "a@example.com", "organizationName": "A org"}})
	if ls, _ := Logins("claude"); len(ls) != 1 || ls[0].User != "a@example.com" || !ls[0].Current || ls[0].Plan != "max" {
		t.Fatalf("logins: %+v", ls)
	}
	// B signs in
	writeFile(t, creds, map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "tok-b", "refreshToken": "ref-b", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "pro"},
		"mcpOAuth":      map[string]any{"keep": true},
	})
	writeFile(t, cfg, map[string]any{"numStartups": 1234567890123, "theme": "dark",
		"oauthAccount": map[string]any{"emailAddress": "b@example.com", "organizationName": "B org"}})
	forgetClaudeCredential()
	if ls, _ := Logins("claude"); len(ls) != 2 || !ls[1].Current {
		t.Fatalf("logins: %+v", ls)
	}
	// B rotates its token before the switch
	writeFile(t, creds, map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "tok-b2", "refreshToken": "ref-b2", "expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "pro"},
		"mcpOAuth":      map[string]any{"keep": true},
	})
	forgetClaudeCredential()
	if _, err := SwitchLogin("cc", "A@example.com"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(creds)
	got, _ := decodeMap(b)
	if o := got["claudeAiOauth"].(map[string]any); o["accessToken"] != "sk-ant-oat01-old" || o["refreshToken"] != "sk-ant-ort01-old" {
		t.Fatalf("credentials: %v", got)
	}
	if got["mcpOAuth"].(map[string]any)["keep"] != true {
		t.Fatalf("other credential fields lost: %v", got)
	}
	cb, _ := os.ReadFile(cfg)
	c, _ := decodeMap(cb)
	if c["theme"] != "dark" || c["numStartups"].(json.Number).String() != "1234567890123" ||
		c["oauthAccount"].(map[string]any)["emailAddress"] != "a@example.com" || c["oauthAccount"].(map[string]any)["organizationName"] != "A org" {
		t.Fatalf("config: %s", cb)
	}
	if e, _ := loginEntry("claude", "b@example.com"); e.Cred["accessToken"] != "tok-b2" || e.Cred["refreshToken"] != "ref-b2" {
		t.Fatalf("outgoing credentials: %v", e.Cred)
	}
	if ls, _ := Logins("claude"); !ls[0].Current || ls[1].Current {
		t.Fatalf("logins: %+v", ls)
	}
	if err := ForgetLogin("claude", "a@example.com"); err == nil {
		t.Fatal("forgot the current account")
	}
	if st, _ := os.Stat(LoginsPath()); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}

func TestAddLoginKeepsCurrentAndTurnsSecondOn(t *testing.T) {
	home := claudeHome(t)
	codexLogin(t, home, "a@example.com", "ra", time.Now().Add(time.Hour))
	old := loginRun
	defer func() { loginRun = old }()
	loginRun = func(ctx context.Context, agent string, out io.Writer) error {
		codexLogin(t, home, "b@example.com", "rb", time.Now().Add(time.Hour))
		return nil
	}
	l, err := AddLogin(context.Background(), "codex", nil)
	if err != nil || l.User != "b@example.com" || l.Current || !l.On {
		t.Fatalf("add: %+v %v", l, err)
	}
	if auth := readAuth(t, home); authRefresh(auth) != "ra" {
		t.Fatalf("the agent's login changed: %v", auth)
	}
	ls, _ := Logins("codex")
	if len(ls) != 2 || !ls[0].Current || ls[1].Current || !ls[1].On || !ls[1].Saved {
		t.Fatalf("logins: %+v", ls)
	}
	if e, _ := loginEntry("codex", "b@example.com"); e.Cred["refresh_token"] != "rb" {
		t.Fatalf("new account credentials: %v", e.Cred)
	}
	if _, err := AddLogin(context.Background(), "gemini", nil); err == nil {
		t.Fatal("gemini accepted")
	}
}

func TestLoginUsage(t *testing.T) {
	home := claudeHome(t)
	codexLogin(t, home, "a@example.com", "ra", time.Now().Add(time.Hour))
	Logins("codex")
	codexLogin(t, home, "b@example.com", "rb", time.Now().Add(time.Hour))
	SetLoginOn("codex", "a@example.com", true)

	hits, failA := 0, false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		id := r.Header.Get("chatgpt-account-id")
		if r.URL.Path != "/wham/usage" {
			w.WriteHeader(404)
			return
		}
		if id == "acct-a@example.com" && failA {
			w.WriteHeader(500)
			return
		}
		used := 10.0
		if id == "acct-b@example.com" {
			used = 55
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": used, "limit_window_seconds": 18000},
			"secondary_window": map[string]any{"used_percent": used / 2, "limit_window_seconds": 604800}}})
	}))
	defer srv.Close()
	oldBase, oldNow := CodexBase, loginUsageNow
	CodexBase = srv.URL + "/codex"
	now := time.Now()
	loginUsageNow = func() time.Time { return now }
	loginUsageCache.Lock()
	loginUsageCache.at, loginUsageCache.data = nil, nil
	loginUsageCache.Unlock()
	defer func() { CodexBase, loginUsageNow = oldBase, oldNow }()

	got := LoginUsage(context.Background(), "codex")
	if len(got) != 2 || got["b@example.com"].Windows[0].Used != 55 || got["a@example.com"].Windows[0].Used != 10 ||
		got["a@example.com"].Windows[0].Name != "5 hours" || got["a@example.com"].Windows[1].Name != "7 days" {
		t.Fatalf("usage: %+v", got)
	}
	n := hits
	LoginUsage(context.Background(), "codex")
	if hits != n {
		t.Fatal("not reused within a minute")
	}
	// later, A's query fails: what was known stays
	failA = true
	now = now.Add(2 * time.Minute)
	got = LoginUsage(context.Background(), "codex")
	if got["a@example.com"].Error != "" || got["a@example.com"].Windows[0].Used != 10 || got["b@example.com"].Windows[0].Used != 55 {
		t.Fatalf("after failure: %+v", got)
	}
	if got := LoginUsage(context.Background(), "gemini"); len(got) != 0 {
		t.Fatalf("gemini: %v", got)
	}
}
