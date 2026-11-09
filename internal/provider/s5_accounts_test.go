package provider

import (
	"context"
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

// s5Usage answers Claude's usage endpoint per bearer token and counts requests.
type s5Usage struct {
	mu    sync.Mutex
	five  map[string]float64
	week  map[string]float64
	fail  map[string]bool
	calls int
}

func (u *s5Usage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if u.fail[tok] {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, `{"five_hour":{"utilization":%g,"resets_at":""},"seven_day":{"utilization":%g,"resets_at":""}}`,
		u.five[tok], u.week[tok])
}

func (u *s5Usage) setFail(tok string, on bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.fail == nil {
		u.fail = map[string]bool{}
	}
	u.fail[tok] = on
}

func (u *s5Usage) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

// s5ResetUsage forgets earlier LoginUsage results, so each test starts cold.
func s5ResetUsage(t *testing.T) {
	t.Helper()
	loginUsageMu.Lock()
	loginUsageCache = map[string]loginUsageEntry{}
	loginUsageMu.Unlock()
	t.Cleanup(func() {
		loginUsageMu.Lock()
		loginUsageCache = map[string]loginUsageEntry{}
		loginUsageMu.Unlock()
	})
}

// s5Expire makes the cached LoginUsage result of an agent older than a minute.
func s5Expire(agent string) {
	loginUsageMu.Lock()
	defer loginUsageMu.Unlock()
	e := loginUsageCache[agent]
	e.at = e.at.Add(-2 * loginUsageTTL)
	loginUsageCache[agent] = e
}

// s5ClaudeSetup signs Claude Code in as a@x.com and remembers b@x.com alongside it.
func s5ClaudeSetup(t *testing.T) {
	t.Helper()
	s4Home(t)
	s4ClaudeEmails(map[string]string{"rA": "a@x.com", "rB": "b@x.com"})
	s4WriteJSON(t, filepath.Join(claudeConfigDir(), ".credentials.json"), map[string]any{
		"claudeAiOauth": s4ClaudeOAuth("rA"),
	})
	if err := saveLogins(loginFile{Accounts: []loginRecord{
		{Agent: "claude", User: "b@x.com", Plan: "pro", On: true, Creds: s4RawMsg(t, s4ClaudeOAuth("rB"))},
	}}); err != nil {
		t.Fatal(err)
	}
}

func s5UsageServer(t *testing.T, u *s5Usage) {
	t.Helper()
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	old := claudeUsageURL
	claudeUsageURL = srv.URL + "/api/oauth/usage"
	t.Cleanup(func() { claudeUsageURL = old })
}

func TestS5LoginUsageReportsEachAccount(t *testing.T) {
	s5ClaudeSetup(t)
	s5ResetUsage(t)
	u := &s5Usage{five: map[string]float64{"at-rA": 16, "at-rB": 70}, week: map[string]float64{"at-rA": 3, "at-rB": 25}}
	s5UsageServer(t, u)

	got := LoginUsage(context.Background(), "claude")
	a, b := got["a@x.com"], got["b@x.com"]
	if len(got) != 2 || a.Error != "" || a.FiveHour != 16 || a.Weekly != 3 {
		t.Fatalf("signed-in account usage: %+v (all %+v)", a, got)
	}
	if b.Error != "" || b.FiveHour != 70 || b.Weekly != 25 || b.Email != "b@x.com" || b.Plan != "pro" {
		t.Fatalf("remembered account usage: %+v", b)
	}
	if len(a.Windows) != 2 || a.Windows[0].Name != "5 hours" || a.Windows[1].Name != "7 days" {
		t.Fatalf("windows: %+v", a.Windows)
	}
}

func TestS5LoginUsageKeepsEarlierResultOnFailure(t *testing.T) {
	s5ClaudeSetup(t)
	s5ResetUsage(t)
	u := &s5Usage{five: map[string]float64{"at-rA": 16, "at-rB": 70}, week: map[string]float64{"at-rA": 3, "at-rB": 25}}
	s5UsageServer(t, u)
	LoginUsage(context.Background(), "claude")

	s5Expire("claude")
	u.setFail("at-rB", true)
	got := LoginUsage(context.Background(), "claude")
	b := got["b@x.com"]
	if b.Error != "" || b.FiveHour != 70 || b.Weekly != 25 {
		t.Fatalf("a failed query replaced the earlier result: %+v", b)
	}
	if a := got["a@x.com"]; a.FiveHour != 16 {
		t.Fatalf("healthy account: %+v", a)
	}
}

func TestS5LoginUsageFailureWithoutEarlierResultIsReported(t *testing.T) {
	s5ClaudeSetup(t)
	s5ResetUsage(t)
	u := &s5Usage{fail: map[string]bool{"at-rB": true}}
	s5UsageServer(t, u)
	got := LoginUsage(context.Background(), "claude")
	if b := got["b@x.com"]; b.Error == "" || b.Email != "b@x.com" {
		t.Fatalf("failed account without an earlier result: %+v", b)
	}
}

func TestS5LoginUsageIsReusedForAMinute(t *testing.T) {
	s5ClaudeSetup(t)
	s5ResetUsage(t)
	u := &s5Usage{five: map[string]float64{"at-rA": 1, "at-rB": 2}}
	s5UsageServer(t, u)

	LoginUsage(context.Background(), "claude")
	first := u.count()
	if first != 2 {
		t.Fatalf("first call queried %d accounts, want 2", first)
	}
	LoginUsage(context.Background(), "claude")
	if u.count() != first {
		t.Fatalf("a result within a minute was not reused: %d queries", u.count())
	}
	s5Expire("claude")
	LoginUsage(context.Background(), "claude")
	if u.count() == first {
		t.Fatal("an expired result was not refreshed")
	}
}

func TestS5LoginUsageUnsupportedAgentIsEmpty(t *testing.T) {
	s5ClaudeSetup(t)
	s5ResetUsage(t)
	for _, agent := range []string{"copilot", "gemini", "nope"} {
		if got := LoginUsage(context.Background(), agent); got == nil || len(got) != 0 {
			t.Fatalf("LoginUsage(%q) = %+v, want an empty map", agent, got)
		}
	}
}

// s5CodexSetup signs Codex in as c@x.com and remembers d@x.com alongside it.
func s5CodexSetup(t *testing.T) string {
	t.Helper()
	home := s4Home(t)
	exp := float64(time.Now().Add(time.Hour).Unix())
	s4WriteJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      s4JWT(map[string]any{"email": "c@x.com"}),
			"access_token":  s4JWT(map[string]any{"exp": exp}),
			"refresh_token": "rC",
			"account_id":    "acct-c",
		},
	})
	if err := saveLogins(loginFile{Accounts: []loginRecord{{Agent: "codex", User: "d@x.com", On: true,
		Creds: s4RawMsg(t, s5CodexCreds("d@x.com", s4JWT(map[string]any{"exp": exp}), "rD", "acct-d"))}}}); err != nil {
		t.Fatal(err)
	}
	return home
}

// s5CodexCreds is a remembered Codex sign-in.
func s5CodexCreds(email, access, refresh, accountID string) map[string]any {
	return map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      s4JWT(map[string]any{"email": email}),
			"access_token":  access,
			"refresh_token": refresh,
			"account_id":    accountID,
		},
	}
}

func TestS5LoginUsageCodexReportsEachAccount(t *testing.T) {
	s5CodexSetup(t)
	s5ResetUsage(t)
	use := map[string][2]float64{"acct-c": {40, 5}, "acct-d": {90, 60}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, ok := use[r.Header.Get("chatgpt-account-id")]
		if r.URL.Path != "/backend-api/wham/usage" || !ok {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":%g,"limit_window_seconds":18000},"secondary_window":{"used_percent":%g,"limit_window_seconds":604800}}}`,
			v[0], v[1])
	}))
	defer srv.Close()
	old := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	defer func() { CodexBase = old }()

	got := LoginUsage(context.Background(), "codex")
	c, d := got["c@x.com"], got["d@x.com"]
	if len(got) != 2 || c.Error != "" || c.FiveHour != 40 || c.Weekly != 5 {
		t.Fatalf("signed-in Codex usage: %+v", c)
	}
	if d.Error != "" || d.FiveHour != 90 || d.Weekly != 60 {
		t.Fatalf("remembered Codex usage: %+v", d)
	}
}

// Accounts that are on come after the one signed in, in the order they were saved.
func TestS5OnAccountsCurrentFirstThenSavedOrder(t *testing.T) {
	home := s5CodexSetup(t)
	exp := float64(time.Now().Add(time.Hour).Unix())
	if err := saveLogins(loginFile{Accounts: []loginRecord{
		{Agent: "codex", User: "z@x.com", On: true, Creds: s4RawMsg(t, s5CodexCreds("z@x.com", s4JWT(map[string]any{"exp": exp}), "rZ", "acct-z"))},
		{Agent: "codex", User: "y@x.com", On: false, Creds: s4RawMsg(t, s5CodexCreds("y@x.com", s4JWT(map[string]any{"exp": exp}), "rY", "acct-y"))},
		{Agent: "codex", User: "x@x.com", On: true, Creds: s4RawMsg(t, s5CodexCreds("x@x.com", s4JWT(map[string]any{"exp": exp}), "rX", "acct-x"))},
		{Agent: "claude", User: "k@x.com", On: true, Creds: s4RawMsg(t, s4ClaudeOAuth("rK"))},
	}}); err != nil {
		t.Fatal(err)
	}
	p, ok := codexAccount(home)
	if !ok {
		t.Fatal("no codex account")
	}
	var users []string
	for _, a := range OnAccounts(p) {
		users = append(users, a.Account.User)
	}
	if strings.Join(users, ",") != "c@x.com,z@x.com,x@x.com" {
		t.Fatalf("candidate order %v", users)
	}

	// a stored account signs with its own token, and the agent's file is untouched
	authPath := filepath.Join(home, ".codex", "auth.json")
	before, _ := os.ReadFile(authPath)
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/responses", nil)
	if err := OnAccounts(p)[1].Sign(context.Background(), req, Responses, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer "+s4JWT(map[string]any{"exp": exp}) || req.Header.Get("chatgpt-account-id") != "acct-z" {
		t.Fatalf("stored account headers: %v", req.Header)
	}
	if after, _ := os.ReadFile(authPath); string(after) != string(before) {
		t.Fatal("signing with a stored account wrote the agent's auth.json")
	}

	// Claude Code is only ever reached through the signed-in account
	cl := Provider{ID: "claude", Account: &Account{Agent: "claude", User: "a@x.com"}}
	if got := OnAccounts(cl); len(got) != 1 {
		t.Fatalf("claude candidates: %d", len(got))
	}
}

// A stored Codex account whose token is about to expire refreshes into the
// store, and the refresh token it was given is the one kept.
func TestS5StoredCodexRefreshStaysInStore(t *testing.T) {
	home := s5CodexSetup(t)
	stale := s4JWT(map[string]any{"exp": float64(time.Now().Add(-time.Hour).Unix())})
	if err := saveLogins(loginFile{Accounts: []loginRecord{{Agent: "codex", User: "y@x.com", On: true,
		Creds: s4RawMsg(t, s5CodexCreds("y@x.com", stale, "rOld", "acct-y"))}}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["refresh_token"] != "rOld" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok-new","refresh_token":"rNew"}`)
	}))
	defer srv.Close()
	old := codexTokenURL
	codexTokenURL = srv.URL + "/token"
	defer func() { codexTokenURL = old }()

	tok, accountID, err := storedCodexToken(context.Background(), "y@x.com")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "tok-new" || accountID != "acct-y" {
		t.Fatalf("token %q account %q", tok, accountID)
	}
	rec := s4FindLogin(t, "codex", "y@x.com")
	if !strings.Contains(string(rec.Creds), `"rNew"`) || strings.Contains(string(rec.Creds), `"rOld"`) {
		t.Fatalf("rotated tokens not kept in the store: %s", rec.Creds)
	}
	if raw, _ := s4ReadRaw(t, filepath.Join(home, ".codex", "auth.json")); raw["tokens"].(map[string]any)["refresh_token"] != "rC" {
		t.Fatal("refreshing a stored account changed the agent's auth.json")
	}
}
