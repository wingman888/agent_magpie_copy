package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rememberCodex writes a remembered Codex account with tokens of its own, as
// if magpie had seen it signed in before.
func rememberCodex(t *testing.T, user string, on bool, refresh string) string {
	t.Helper()
	access := fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())})
	login, _ := json.Marshal(map[string]any{
		"id_token":      fakeJWT(map[string]any{"email": user}),
		"access_token":  access,
		"refresh_token": refresh,
		"account_id":    "acct-" + refresh,
	})
	f := loadLogins()
	f.Logins = append(f.Logins, loginRecord{Agent: "codex", User: user, Plan: "plus", On: on, Login: login})
	if err := storeLogins(f); err != nil {
		t.Fatal(err)
	}
	return access
}

func readAuth(t *testing.T, home string) map[string]any {
	t.Helper()
	var raw map[string]any
	if !readJSON(codexAuthPath(home), &raw) {
		t.Fatal("no codex auth.json")
	}
	return raw
}

func savedRecord(t *testing.T, user string) loginRecord {
	t.Helper()
	f := loadLogins()
	i := findLogin(f, "codex", user)
	if i < 0 {
		t.Fatalf("no record for %s", user)
	}
	return f.Logins[i]
}

func TestSetLoginOnRefusesTheAccountSignedIn(t *testing.T) {
	signIn(t)
	if err := SetLoginOn("codex", "me@example.com", false); err == nil {
		t.Fatal("turning off the account signed in should fail")
	}
	if err := SetLoginOn("codex", "nobody@example.com", true); err == nil {
		t.Fatal("an unknown account should fail")
	}
	if err := SetLoginOn("gemini", "me@example.com", true); err == nil {
		t.Fatal("an agent without remembered accounts should fail")
	}
	if err := SetLoginOn("codex", "ME@example.com", true); err != nil {
		t.Fatalf("turning on the signed-in account: %v", err)
	}
}

func TestLoginsMarksTheSignedInAccount(t *testing.T) {
	signIn(t)
	rememberCodex(t, "other@example.com", true, "r2")
	logins, err := Logins("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(logins) != 2 {
		t.Fatalf("logins = %+v, want two", logins)
	}
	var cur, other Login
	for _, l := range logins {
		if l.Current {
			cur = l
		} else {
			other = l
		}
	}
	if cur.User != "me@example.com" || !cur.On || cur.Plan != "pro" {
		t.Fatalf("current = %+v", cur)
	}
	if other.User != "other@example.com" || !other.On || other.Current {
		t.Fatalf("other = %+v", other)
	}
	if _, err := Logins("gemini"); err == nil {
		t.Fatal("unknown agent should fail")
	}
}

// Switching keeps the tokens of the account it replaces, moves the target's
// tokens into the agent's store, and leaves every other field there alone.
func TestSwitchKeepsOutgoingTokensAndOtherFields(t *testing.T) {
	home := signIn(t)
	raw := readAuth(t, home)
	raw["OPENAI_API_KEY"] = nil
	raw["custom"] = "keep me"
	writeFile(t, codexAuthPath(home), raw)

	otherAccess := rememberCodex(t, "other@example.com", false, "r2")
	outgoing := readAuth(t, home)["tokens"].(map[string]any)

	if err := SwitchLogin("codex", "OTHER@example.com"); err != nil {
		t.Fatal(err)
	}

	after := readAuth(t, home)
	if after["custom"] != "keep me" {
		t.Fatalf("an unrelated field changed: %v", after["custom"])
	}
	if _, ok := after["OPENAI_API_KEY"]; !ok {
		t.Fatal("OPENAI_API_KEY dropped")
	}
	tokens := after["tokens"].(map[string]any)
	if tokens["access_token"] != otherAccess || tokens["refresh_token"] != "r2" || tokens["account_id"] != "acct-r2" {
		t.Fatalf("agent now holds %v, want the other account's tokens", tokens)
	}

	me := savedRecord(t, "me@example.com")
	var kept map[string]any
	json.Unmarshal(me.Login, &kept)
	if kept["refresh_token"] != outgoing["refresh_token"] || kept["access_token"] != outgoing["access_token"] {
		t.Fatalf("outgoing tokens not kept: %v", kept)
	}
	if me.On {
		t.Fatal("the replaced account should inherit the target's off state")
	}
	if got := savedRecord(t, "other@example.com").Login; len(got) != 0 {
		t.Fatalf("the signed-in account should hold no copy of its tokens, has %s", got)
	}

	info, err := os.Stat(loginsPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("accounts.json mode = %v, want 0600", info.Mode().Perm())
	}

	logins, _ := Logins("codex")
	for _, l := range logins {
		if l.Current != strings.EqualFold(l.User, "other@example.com") {
			t.Fatalf("current marker wrong: %+v", logins)
		}
	}
}

func TestSwitchRejectsUnknownAccount(t *testing.T) {
	signIn(t)
	if err := SwitchLogin("codex", "nobody@example.com"); err == nil {
		t.Fatal("switching to an unknown account should fail")
	}
	if err := SwitchLogin("gemini", "me@example.com"); err == nil {
		t.Fatal("an unknown agent should fail")
	}
}

func TestForgetRefusesTheSignedInAccount(t *testing.T) {
	signIn(t)
	rememberCodex(t, "other@example.com", true, "r2")
	if err := ForgetLogin("codex", "me@example.com"); err == nil {
		t.Fatal("forgetting the signed-in account should fail")
	}
	if err := ForgetLogin("codex", "other@example.com"); err != nil {
		t.Fatal(err)
	}
	logins, _ := Logins("codex")
	if len(logins) != 1 || !logins[0].Current {
		t.Fatalf("after forget: %+v", logins)
	}
}

// Only switched-on accounts that hold their own tokens are candidates for the
// gateway, after the one signed in.
func TestCodexAlternatesAreOnAccountsWithTokens(t *testing.T) {
	signIn(t)
	rememberCodex(t, "second@example.com", true, "r2")
	rememberCodex(t, "third@example.com", false, "r3")
	rememberCodex(t, "fourth@example.com", true, "r4")
	f := loadLogins()
	f.Logins[findLogin(f, "codex", "fourth@example.com")].Login = nil
	storeLogins(f)

	a, ok := codexAccount(os.Getenv("HOME"))
	if !ok {
		t.Fatal("no codex account")
	}
	alts := a.Account.Alternates()
	if len(alts) != 1 || alts[0].Account.User != "second@example.com" {
		t.Fatalf("alternates = %+v, want only second@example.com", alts)
	}
	if alts[0].Responses != CodexBase || alts[0].ID != "codex" {
		t.Fatalf("alternate is not a codex provider: %+v", alts[0])
	}
	if got := Accounts(); len(got) != 2 {
		t.Fatalf("remembered accounts must not be listed as providers: %d", len(got))
	}
}

// LoginUsage reports each account's windows, reuses a result for a minute,
// and keeps what it knew of an account whose lookup fails.
func TestLoginUsageCachesAndKeepsKnownResults(t *testing.T) {
	signIn(t)
	rememberCodex(t, "other@example.com", true, "r2")

	var calls atomic.Int32
	var failOther atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/wham/usage") {
			http.NotFound(w, r)
			return
		}
		if failOther.Load() && r.Header.Get("chatgpt-account-id") == "acct-r2" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		used := 12.0
		if r.Header.Get("chatgpt-account-id") == "acct-r2" {
			used = 55
		}
		json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "plus",
			"rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": used, "limit_window_seconds": 5 * 60 * 60},
				"secondary_window": map[string]any{"used_percent": used * 2, "limit_window_seconds": 7 * 24 * 60 * 60},
			},
		})
	}))
	defer srv.Close()
	oldBase := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = oldBase })
	resetLoginUsage := func() {
		loginUsageCache.Lock()
		loginUsageCache.at = nil
		loginUsageCache.data = nil
		loginUsageCache.Unlock()
	}
	resetLoginUsage()
	t.Cleanup(resetLoginUsage)

	ctx := context.Background()
	got := LoginUsage(ctx, "codex")
	if len(got) != 2 {
		t.Fatalf("usage = %+v, want two accounts", got)
	}
	me := got["me@example.com"]
	if me.FiveHour == nil || *me.FiveHour != 12 || me.Weekly == nil || *me.Weekly != 24 {
		t.Fatalf("me = %+v", me)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("first call made %d requests, want 2", n)
	}

	LoginUsage(ctx, "codex")
	if n := calls.Load(); n != 2 {
		t.Fatalf("a second call within a minute made %d requests in total, want 2", n)
	}

	resetTime := func() {
		loginUsageCache.Lock()
		loginUsageCache.at["codex"] = time.Now().Add(-2 * time.Minute)
		loginUsageCache.Unlock()
	}
	failOther.Store(true)
	resetTime()
	again := LoginUsage(ctx, "codex")
	other := again["other@example.com"]
	if other.Error != "" || other.FiveHour == nil || *other.FiveHour != 55 {
		t.Fatalf("a failed lookup should keep the earlier result, got %+v", other)
	}
	if again["me@example.com"].FiveHour == nil {
		t.Fatal("the account that succeeded lost its result")
	}

	if len(LoginUsage(ctx, "gemini")) != 0 {
		t.Fatal("an unsupported agent should have no usage")
	}
}

func TestSavedCodexTokenKeepsFreshTokensWithoutRefresh(t *testing.T) {
	signIn(t)
	access := rememberCodex(t, "other@example.com", true, "r2")
	tok, accountID, err := savedCodexToken(context.Background(), "other@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if tok != access || accountID != "acct-r2" {
		t.Fatalf("token = %q account = %q", tok, accountID)
	}
}
