package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Claude Code accounts cannot be turned on alongside the sign-in.
func TestS5ClaudeOnIsRefused(t *testing.T) {
	magS3Env(t)
	err := SetLoginOn("claude", "c2@example.com", true)
	if err == nil || !strings.Contains(err.Error(), "暂不支持同时生效") {
		t.Fatalf("SetLoginOn(claude, true) = %v, want the not-supported error", err)
	}
	// turning off stays as it was: an unknown account is still reported
	if err := SetLoginOn("claude", "nobody@example.com", false); err == nil {
		t.Fatal("an unknown claude account was turned off without error")
	}
}

// A saved copy of a non-current Claude sign-in is queried with its own token,
// and a failed or expired copy keeps the last known usage.
func TestS5ClaudeSavedCopyUsage(t *testing.T) {
	magS3Env(t)
	fresh := time.Now().Add(24 * time.Hour).UnixMilli()
	saveCopy := func(user, token string, expires int64) {
		t.Helper()
		login := map[string]any{"claudeAiOauth": map[string]any{"accessToken": token,
			"refreshToken": "r-" + token, "expiresAt": expires, "subscriptionType": "max"}}
		if _, err := magWriteStash("claude", user, login); err != nil {
			t.Fatal(err)
		}
	}
	saveCopy("c2@example.com", "tok-c2", fresh)
	if err := magStoreAccounts(magAccountsFile{Accounts: []magRememberedAccount{
		{Agent: "claude", User: "c2@example.com", Plan: "max", Saved: true},
	}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	failC2 := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok-c2" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if failC2 {
			http.Error(w, "busy", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"five_hour":{"utilization":33,"resets_at":"2030-01-01T00:00:00Z"},"seven_day":{"utilization":61,"resets_at":"2030-01-05T00:00:00Z"}}`)
	}))
	defer srv.Close()
	oldURL := claudeUsageURL
	claudeUsageURL = srv.URL + "/api/oauth/usage"
	t.Cleanup(func() { claudeUsageURL = oldURL })

	ctx := context.Background()
	got := LoginUsage(ctx, "claude")
	u, ok := got["c2@example.com"]
	if !ok || u.Error != "" || len(u.Windows) != 2 || u.Windows[0].Used != 33 || u.Windows[1].Used != 61 {
		t.Fatalf("c2 usage = %+v", u)
	}
	if u.Plan != "max" {
		t.Fatalf("c2 plan = %q", u.Plan)
	}

	mu.Lock()
	failC2 = true
	mu.Unlock()
	magLoginUsageCache.Lock()
	for _, e := range magLoginUsageCache.by {
		e.at = time.Time{}
	}
	magLoginUsageCache.Unlock()
	again := LoginUsage(ctx, "claude")
	if u := again["c2@example.com"]; u.Error != "" || len(u.Windows) != 2 || u.Windows[0].Used != 33 {
		t.Fatalf("a failed query should keep c2's last known usage: %+v", u)
	}

	// an expired copy is a failed query, not a panic; the last known usage stays
	saveCopy("c2@example.com", "tok-c2", time.Now().Add(-time.Hour).UnixMilli())
	magLoginUsageCache.Lock()
	for _, e := range magLoginUsageCache.by {
		e.at = time.Time{}
	}
	magLoginUsageCache.Unlock()
	expired := LoginUsage(ctx, "claude")
	if u := expired["c2@example.com"]; u.Error != "" || len(u.Windows) != 2 || u.Windows[0].Used != 33 {
		t.Fatalf("an expired copy should keep c2's last known usage: %+v", u)
	}
}
