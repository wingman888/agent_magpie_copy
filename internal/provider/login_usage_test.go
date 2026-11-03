package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// usageServer stands in for both vendors' usage endpoints. Each sign-in is keyed
// by what its request carries: the account id for Codex, the bearer token for
// Claude Code. Anything else, or a key marked failing, gets a 500.
type usageServer struct {
	mu   sync.Mutex
	pct  map[string][2]float64 // five-hour and weekly used percent
	fail map[string]bool
	hang bool
	hits int
}

func (u *usageServer) set(key string, five, week float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pct[key] = [2]float64{five, week}
}

func (u *usageServer) setFail(key string, fail bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.fail[key] = fail
}

func (u *usageServer) setHang(hang bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hang = hang
}

func (u *usageServer) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits
}

func (u *usageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.hits++
	hang := u.hang
	u.mu.Unlock()
	if hang {
		<-r.Context().Done()
		return
	}
	key, claude := r.Header.Get("chatgpt-account-id"), false
	if strings.HasSuffix(r.URL.Path, "/oauth/usage") {
		key, claude = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), true
	}
	u.mu.Lock()
	p, ok := u.pct[key]
	failing := u.fail[key]
	u.mu.Unlock()
	if !ok || failing {
		http.Error(w, "unavailable", http.StatusInternalServerError)
		return
	}
	if claude {
		fmt.Fprintf(w, `{"five_hour":{"utilization":%v},"seven_day":{"utilization":%v}}`, p[0], p[1])
		return
	}
	fmt.Fprintf(w, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":%v,"limit_window_seconds":18000},"secondary_window":{"used_percent":%v,"limit_window_seconds":604800}}}`, p[0], p[1])
}

// newUsageServer points both vendors' usage endpoints at a fake and starts with
// an empty LoginUsage cache.
func newUsageServer(t *testing.T) *usageServer {
	t.Helper()
	u := &usageServer{pct: map[string][2]float64{}, fail: map[string]bool{}}
	srv := httptest.NewServer(u)
	t.Cleanup(srv.Close)
	oldCodex, oldClaude := CodexBase, claudeUsageURL
	CodexBase, claudeUsageURL = srv.URL+"/codex", srv.URL+"/api/oauth/usage"
	t.Cleanup(func() { CodexBase, claudeUsageURL = oldCodex, oldClaude })
	resetLoginUsage(t)
	return u
}

func resetLoginUsage(t *testing.T) {
	t.Helper()
	reset := func() {
		c := &loginUsageCache
		c.Lock()
		c.byAgent = nil
		c.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// expireLoginUsage makes the cached result for agent older than the minute.
func expireLoginUsage(agent string) {
	c := &loginUsageCache
	c.Lock()
	defer c.Unlock()
	if st := c.byAgent[agent]; st != nil {
		st.at = time.Now().Add(-2 * time.Minute)
	}
}

// savedCodexLogin remembers a second Codex sign-in, with its own tokens.
func savedCodexLogin(t *testing.T, user, acct string) {
	t.Helper()
	creds, _ := json.Marshal(map[string]any{
		"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
		"refresh_token": "refresh-" + acct, "account_id": acct,
	})
	writeFile(t, accountsPath(), accountsFile{Accounts: []savedAccount{{Agent: "codex", User: user, Plan: "plus", On: true, Creds: creds}}})
}

// checkUsage wants exactly the sign-ins in want, each with its 5 hour and weekly
// used percentages, all from provider agent.
func checkUsage(t *testing.T, got map[string]SubscriptionQuota, agent string, want map[string][2]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d sign-ins %+v, want %d", len(got), got, len(want))
	}
	for user, p := range want {
		q, ok := got[user]
		if !ok {
			t.Fatalf("no entry for %s in %+v", user, got)
		}
		if q.Provider != agent || q.Error != "" || len(q.Windows) != 2 ||
			q.Windows[0].Name != "5 hours" || q.Windows[0].Used != p[0] ||
			q.Windows[1].Name != "7 days" || q.Windows[1].Used != p[1] {
			t.Fatalf("%s: %+v, want 5 hours %v and 7 days %v", user, q, p[0], p[1])
		}
	}
}

func TestLoginUsageAsksEachCodexSignInWithItsOwnCredentials(t *testing.T) {
	home := savedHome(t)
	u := newUsageServer(t)
	codexSignIn(t, home, "cur@example.com", "acct-cur")
	savedCodexLogin(t, "old@example.com", "acct-old")
	u.set("acct-cur", 30, 70)
	u.set("acct-old", 5, 10)

	checkUsage(t, LoginUsage(context.Background(), "codex"), "codex", map[string][2]float64{
		"cur@example.com": {30, 70},
		"old@example.com": {5, 10},
	})
}

func TestLoginUsageReadsClaudeSignIns(t *testing.T) {
	home := savedHome(t)
	u := newUsageServer(t)
	fakeClaudeWho(t, map[string]string{"tok-a": "a@example.com", "tok-b": "b@example.com"})
	writeClaudeLogin(t, home, "tok-a")
	creds, _ := json.Marshal(map[string]any{
		"accessToken": "tok-b", "refreshToken": "refresh-b",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(),
	})
	writeFile(t, accountsPath(), accountsFile{Accounts: []savedAccount{{Agent: "claude", User: "b@example.com", Plan: "max", On: true, Creds: creds}}})
	u.set("tok-a", 12, 40)
	u.set("tok-b", 1, 2)

	// cc is the alias for claude
	checkUsage(t, LoginUsage(context.Background(), "cc"), "claude", map[string][2]float64{
		"a@example.com": {12, 40},
		"b@example.com": {1, 2},
	})
}

// A sign-in whose lookup fails keeps its last known result, and the others
// still update.
func TestLoginUsageKeepsLastKnownResultWhenASignInFails(t *testing.T) {
	home := savedHome(t)
	u := newUsageServer(t)
	codexSignIn(t, home, "cur@example.com", "acct-cur")
	savedCodexLogin(t, "old@example.com", "acct-old")
	u.set("acct-cur", 30, 70)
	u.set("acct-old", 5, 10)
	checkUsage(t, LoginUsage(context.Background(), "codex"), "codex", map[string][2]float64{
		"cur@example.com": {30, 70},
		"old@example.com": {5, 10},
	})

	u.set("acct-cur", 50, 60)
	u.setFail("acct-old", true)
	expireLoginUsage("codex")
	got := LoginUsage(context.Background(), "codex")
	checkUsage(t, got, "codex", map[string][2]float64{
		"cur@example.com": {50, 60},
		"old@example.com": {5, 10},
	})
	if got["old@example.com"].Error != "" {
		t.Fatalf("failed sign-in lost its last known result: %+v", got["old@example.com"])
	}
}

func TestLoginUsageReusesResultsForAMinute(t *testing.T) {
	home := savedHome(t)
	u := newUsageServer(t)
	codexSignIn(t, home, "cur@example.com", "acct-cur")
	savedCodexLogin(t, "old@example.com", "acct-old")
	u.set("acct-cur", 30, 70)
	u.set("acct-old", 5, 10)

	LoginUsage(context.Background(), "codex")
	before := u.count()
	if before != 2 {
		t.Fatalf("first call made %d requests, want one per sign-in", before)
	}
	LoginUsage(context.Background(), "codex")
	if u.count() != before {
		t.Fatalf("a call within a minute made %d more requests", u.count()-before)
	}
	expireLoginUsage("codex")
	LoginUsage(context.Background(), "codex")
	if u.count() == before {
		t.Fatal("a call after a minute reused the old result")
	}
}

func TestLoginUsageUnsupportedAgentIsEmpty(t *testing.T) {
	savedHome(t)
	u := newUsageServer(t)
	for _, agent := range []string{"copilot", "gemini", ""} {
		got := LoginUsage(context.Background(), agent)
		if got == nil || len(got) != 0 {
			t.Fatalf("LoginUsage(%q) = %+v, want an empty map", agent, got)
		}
	}
	if u.count() != 0 {
		t.Fatalf("unsupported agents reached the upstream %d times", u.count())
	}
}

// Cancelling stops the wait; the cancelled result is not cached, so the next
// call asks again.
func TestLoginUsageStopsWhenCancelled(t *testing.T) {
	home := savedHome(t)
	u := newUsageServer(t)
	codexSignIn(t, home, "cur@example.com", "acct-cur")
	u.setHang(true)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := LoginUsage(ctx, "codex")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancelled call took %v", d)
	}
	if q, ok := got["cur@example.com"]; !ok || q.Error == "" {
		t.Fatalf("cancelled call returned %+v, want the sign-in with an error", got)
	}

	u.setHang(false)
	u.set("acct-cur", 30, 70)
	checkUsage(t, LoginUsage(context.Background(), "codex"), "codex", map[string][2]float64{
		"cur@example.com": {30, 70},
	})
}
