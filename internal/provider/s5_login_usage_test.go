package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// s5ResetUsage empties the LoginUsage cache, now and when the test ends.
func s5ResetUsage(t *testing.T) {
	t.Helper()
	reset := func() {
		loginUsageCache.Lock()
		loginUsageCache.byAgent = map[string]loginUsageEntry{}
		loginUsageCache.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// s5Expire makes the cached result of agent old enough to be refreshed.
func s5Expire(agent string) {
	loginUsageCache.Lock()
	e := loginUsageCache.byAgent[agent]
	e.at = time.Now().Add(-2 * time.Minute)
	loginUsageCache.byAgent[agent] = e
	loginUsageCache.Unlock()
}

// s5Usage serves codex usage per bearer token and counts the requests. A
// token in failing answers 500.
type s5Usage struct {
	mu      sync.Mutex
	hits    int
	failing map[string]bool
	windows map[string][2]float64 // token -> 5-hour, weekly used percent
}

func (u *s5Usage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hits++
	tok := r.Header.Get("Authorization")
	if len(tok) > len("Bearer ") {
		tok = tok[len("Bearer "):]
	}
	if u.failing[tok] {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	v, ok := u.windows[tok]
	if !ok {
		http.Error(w, "no", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/wham/usage" && r.Header.Get("chatgpt-account-id") == "" {
		http.Error(w, "no account", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"plan_type":"plus","rate_limit":{` +
		`"primary_window":{"used_percent":` + s5Num(v[0]) + `,"limit_window_seconds":18000},` +
		`"secondary_window":{"used_percent":` + s5Num(v[1]) + `,"limit_window_seconds":604800}}}`))
}

func (u *s5Usage) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

func (u *s5Usage) fail(tok string, on bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failing == nil {
		u.failing = map[string]bool{}
	}
	u.failing[tok] = on
}

func s5Num(f float64) string {
	return fmt.Sprintf("%g", f)
}

// s5CodexAccounts signs in a.codex@x as the agent's login and saves b as a
// second login; the usage endpoint is served by u.
func s5CodexAccounts(t *testing.T, u *s5Usage) {
	t.Helper()
	s4Home(t)
	s5ResetUsage(t)
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	old := codexUsageURL
	codexUsageURL = srv.URL + "/wham/usage"
	t.Cleanup(func() { codexUsageURL = old })

	s4Codex(t, codexAuthPath(), "a@x.com", "tokA")
	s4Codex(t, loginTokens("codex", "b@x.com"), "b@x.com", "tokB")
	if err := storeLogins([]savedLogin{{Agent: "codex", User: "b@x.com", Plan: "plus", On: true}}); err != nil {
		t.Fatal(err)
	}
	u.windows = map[string][2]float64{"tokA": {10, 30}, "tokB": {50, 70}}
}

func TestS5LoginUsagePerAccount(t *testing.T) {
	u := &s5Usage{}
	s5CodexAccounts(t, u)

	got := LoginUsage(context.Background(), "codex")
	if len(got) != 2 {
		t.Fatalf("got %d accounts, want 2: %+v", len(got), got)
	}
	if a := got["a@x.com"]; a.FiveHour != 10 || a.Weekly != 30 || len(a.Windows) != 2 {
		t.Errorf("a@x.com = %+v, want 5h 10 weekly 30", a)
	}
	if b := got["b@x.com"]; b.FiveHour != 50 || b.Weekly != 70 || b.Error != "" {
		t.Errorf("b@x.com = %+v, want 5h 50 weekly 70", b)
	}
}

func TestS5LoginUsageKeepsEarlierResultOnFailure(t *testing.T) {
	u := &s5Usage{}
	s5CodexAccounts(t, u)
	LoginUsage(context.Background(), "codex")

	u.fail("tokB", true)
	s5Expire("codex")
	got := LoginUsage(context.Background(), "codex")
	b, ok := got["b@x.com"]
	if !ok {
		t.Fatalf("b@x.com dropped after a failed query: %+v", got)
	}
	if b.FiveHour != 50 || b.Weekly != 70 {
		t.Errorf("b@x.com = %+v, want the earlier 5h 50 weekly 70", b)
	}
	if a := got["a@x.com"]; a.FiveHour != 10 {
		t.Errorf("a@x.com = %+v, want 5h 10", a)
	}
}

func TestS5LoginUsageFailureWithoutEarlierResultReportsError(t *testing.T) {
	u := &s5Usage{}
	s5CodexAccounts(t, u)
	u.fail("tokB", true)

	got := LoginUsage(context.Background(), "codex")
	b, ok := got["b@x.com"]
	if !ok {
		t.Fatalf("b@x.com missing: %+v", got)
	}
	if b.Error == "" {
		t.Errorf("b@x.com = %+v, want an error", b)
	}
}

func TestS5LoginUsageReusesResultForAMinute(t *testing.T) {
	u := &s5Usage{}
	s5CodexAccounts(t, u)

	LoginUsage(context.Background(), "codex")
	first := u.count()
	if first == 0 {
		t.Fatal("first call did not query the endpoint")
	}
	LoginUsage(context.Background(), "codex")
	if n := u.count(); n != first {
		t.Fatalf("second call within a minute queried again: %d -> %d", first, n)
	}
	s5Expire("codex")
	LoginUsage(context.Background(), "codex")
	if n := u.count(); n <= first {
		t.Fatalf("expired result was not refreshed: %d -> %d", first, n)
	}
}

func TestS5LoginUsageClaudeAccount(t *testing.T) {
	s4Home(t)
	s5ResetUsage(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tokC" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"five_hour":{"utilization":25},"seven_day":{"utilization":40}}`))
	}))
	t.Cleanup(srv.Close)
	old := claudeUsageURL
	claudeUsageURL = srv.URL
	t.Cleanup(func() { claudeUsageURL = old })

	s4Claude(t, loginTokens("claude", "c@x.com"), "tokC")
	if err := storeLogins([]savedLogin{{Agent: "claude", User: "c@x.com", Plan: "pro"}}); err != nil {
		t.Fatal(err)
	}
	got := LoginUsage(context.Background(), "claude")
	if c := got["c@x.com"]; c.FiveHour != 25 || c.Weekly != 40 {
		t.Errorf("c@x.com = %+v, want 5h 25 weekly 40", got)
	}
}

func TestS5LoginUsageUnsupportedAgentIsEmpty(t *testing.T) {
	s4Home(t)
	s5ResetUsage(t)
	for _, agent := range []string{"copilot", "gemini", ""} {
		if got := LoginUsage(context.Background(), agent); got == nil || len(got) != 0 {
			t.Errorf("LoginUsage(%q) = %+v, want an empty map", agent, got)
		}
	}
}
