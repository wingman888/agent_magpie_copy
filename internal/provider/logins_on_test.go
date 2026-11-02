package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionsOnAtOnce(t *testing.T) {
	home := signIn(t)
	// a saved account whose access token has run out: it's refreshed in
	// logins.json when it's used, never put back into the agent's store
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "old@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(-time.Hour).Unix())}),
			"refresh_token": "r-old", "account_id": "acct-old",
		},
	})
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)

	var refreshed string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		refreshed = body["refresh_token"]
		json.NewEncoder(w).Encode(map[string]any{"access_token": "fresh-old", "refresh_token": "r-old-2"})
	}))
	defer fake.Close()
	old := codexTokenURL
	codexTokenURL = fake.URL
	t.Cleanup(func() { codexTokenURL = old })

	p, err := Find("codex")
	if err != nil || p.Account == nil || p.Account.User != "work@example.com" {
		t.Fatalf("codex: %+v %v", p, err)
	}
	if n := len(p.AlsoOn()); n != 0 {
		t.Fatalf("%d accounts on before any was turned on", n)
	}
	if err := SetLoginOn("codex", "work@example.com", false); err == nil {
		t.Fatal("turned off the account codex is signed in to")
	}
	if err := SetLoginOn("codex", "OLD@example.com", true); err != nil {
		t.Fatal(err)
	}
	// me@ and old@ are both saved; only old@ is on
	also := p.AlsoOn()
	if len(also) != 1 {
		t.Fatalf("%d accounts also on", len(also))
	}
	q := also[0]
	if q.ID != "codex" || q.Account.User != "old@example.com" || p.Account.User != "work@example.com" {
		t.Fatalf("copy %s %s, primary %s", q.ID, q.Account.User, p.Account.User)
	}
	tok, ok, err := q.Account.Token(context.Background())
	if err != nil || !ok || tok != "fresh-old" || refreshed != "r-old" {
		t.Fatalf("token %q %v %v, refreshed with %q", tok, ok, err, refreshed)
	}
	req, _ := http.NewRequest("POST", "http://x", nil)
	if err := q.Account.sign(context.Background(), req, nil); err != nil || req.Header.Get("Authorization") != "Bearer fresh-old" || req.Header.Get("chatgpt-account-id") != "acct-old" {
		t.Fatalf("signed %v %v", req.Header, err)
	}
	for _, l := range readLogins() {
		if l.User == "old@example.com" && !strings.Contains(string(l.Auth), "r-old-2") {
			t.Fatalf("the refresh wasn't kept: %s", l.Auth)
		}
	}
	var live codexAuth
	readJSON(filepath.Join(home, ".codex", "auth.json"), &live)
	if live.Tokens.RefreshToken != "r-work" {
		t.Fatalf("the agent's own sign-in changed: %+v", live.Tokens)
	}
	if _, ok, _ := p.Account.Token(context.Background()); ok {
		t.Fatal("the agent's own account has a token of magpie's")
	}

	// switching keeps the one it replaces in use
	if err := SwitchLogin("codex", "old@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, l := range Logins("codex") {
		if l.User == "work@example.com" && !l.On || l.User == "me@example.com" && l.On {
			t.Fatalf("after switch: %+v", l)
		}
	}
	if err := SetLoginOn("codex", "work@example.com", false); err != nil {
		t.Fatal(err)
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("turned on an unknown account")
	}
}

func TestLoginUsageEachAccount(t *testing.T) {
	home := signIn(t)
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	used := map[string]float64{"acct-1": 12, "acct-work@example.com": 97}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": used[r.Header.Get("chatgpt-account-id")], "limit_window_seconds": 18000}}})
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	u := LoginUsage(context.Background(), "codex")
	if len(u) != 2 || u["me@example.com"].Windows[0].Used != 12 || u["work@example.com"].Windows[0].Used != 97 || u["work@example.com"].Windows[0].Name != "5 hours" {
		t.Fatalf("usage %+v", u)
	}
	if len(LoginUsage(context.Background(), "copilot")) != 0 {
		t.Fatal("usage for an agent without accounts")
	}
}
