package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A refresh of a remembered Claude sign-in runs over the network. It must not
// hold the lock on accounts.json while it waits, or every other provider lookup
// stalls behind it.
func TestSavedRefreshDoesNotHoldStoreLock(t *testing.T) {
	savedHome(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new-acc", "refresh_token": "r-new", "expires_in": 3600})
	}))
	defer srv.Close()
	old := claudeTokenURL
	claudeTokenURL = srv.URL
	t.Cleanup(func() { claudeTokenURL = old })

	writeFile(t, accountsPath(), map[string]any{"accounts": []map[string]any{{
		"agent": "claude", "user": "other@example.com", "on": true,
		"creds": map[string]any{"accessToken": "old-acc", "refreshToken": "r-old",
			"expiresAt": time.Now().Add(-time.Hour).UnixMilli(), "subscriptionType": "max"},
	}}})

	type result struct {
		tok string
		err error
	}
	done := make(chan result, 1)
	go func() {
		tok, err := savedClaudeToken(context.Background(), "other@example.com")
		done <- result{tok, err}
	}()
	<-entered
	if !accountsMu.TryLock() {
		t.Fatal("accounts.json stays locked for the whole token refresh")
	}
	accountsMu.Unlock()
	close(release)
	res := <-done
	if res.err != nil || res.tok != "new-acc" {
		t.Fatalf("token = %q, %v; want the refreshed one", res.tok, res.err)
	}
	if got := storedAccess("claude", "other@example.com"); got != "new-acc" {
		t.Fatalf("store holds %q after refresh, want new-acc", got)
	}
}

// Codex's auth.json is shared with the Codex CLI. A file magpie cannot parse is
// left alone rather than replaced, and a stated auth mode is kept.
func TestCodexSwitchKeepsFileItCannotParse(t *testing.T) {
	home := savedHome(t)
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	garbage := []byte(`{"tokens": not json`)
	if err := os.WriteFile(path, garbage, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeAgentCreds("codex", json.RawMessage(`{"access_token":"a","refresh_token":"r"}`)); err == nil {
		t.Fatal("switch over an unreadable auth.json succeeded")
	}
	if b, _ := os.ReadFile(path); string(b) != string(garbage) {
		t.Fatalf("auth.json was rewritten: %q", b)
	}
}

func TestCodexSwitchKeepsStatedAuthMode(t *testing.T) {
	home := savedHome(t)
	path := filepath.Join(home, ".codex", "auth.json")
	writeFile(t, path, map[string]any{"auth_mode": "apikey", "x_custom": "keep"})
	if err := writeAgentCreds("codex", json.RawMessage(`{"access_token":"a"}`)); err != nil {
		t.Fatal(err)
	}
	raw := readRawMap(path)
	if raw["auth_mode"] != "apikey" || raw["x_custom"] != "keep" {
		t.Fatalf("auth.json = %v; auth_mode and other fields must stay", raw)
	}
	// with no mode stated, a ChatGPT sign-in is the one written
	writeFile(t, path, map[string]any{"x_custom": "keep"})
	if err := writeAgentCreds("codex", json.RawMessage(`{"access_token":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if raw := readRawMap(path); raw["auth_mode"] != "chatgpt" || raw["x_custom"] != "keep" {
		t.Fatalf("auth.json = %v; want auth_mode chatgpt with the other field kept", raw)
	}
}
