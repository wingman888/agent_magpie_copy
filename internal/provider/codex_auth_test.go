package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCodexUpRefreshWritesBack: an access token close to expiry is refreshed
// with the refresh token, and the new tokens replace the old ones in
// ~/.codex/auth.json while every other field stays.
func TestCodexUpRefreshWritesBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	jwt := func(claims map[string]any) string {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		b, _ := json.Marshal(claims)
		return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	doc := map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": "keep-me",
		"tokens": map[string]any{
			"id_token":      "id-old",
			"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Minute).Unix()}),
			"refresh_token": "rt-old",
			"account_id":    "acct-1",
		},
	}
	b, _ := json.Marshal(doc)
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sent)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"at-new","refresh_token":"rt-new"}`)
	}))
	defer srv.Close()
	oldURL := codexRefreshURL
	codexRefreshURL = srv.URL
	defer func() { codexRefreshURL = oldURL }()

	tok, acct, err := CodexAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "at-new" || acct != "acct-1" {
		t.Errorf("token %q account %q", tok, acct)
	}
	if sent["grant_type"] != "refresh_token" || sent["refresh_token"] != "rt-old" || sent["client_id"] != codexClientID {
		t.Errorf("refresh request = %v", sent)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(saved, &got); err != nil {
		t.Fatal(err)
	}
	tokens, _ := got["tokens"].(map[string]any)
	if tokens["access_token"] != "at-new" || tokens["refresh_token"] != "rt-new" || tokens["id_token"] != "id-old" || tokens["account_id"] != "acct-1" {
		t.Errorf("tokens after refresh = %v", tokens)
	}
	if got["last_refresh"] != "keep-me" || got["auth_mode"] != "chatgpt" {
		t.Errorf("other fields changed: %v", got)
	}
}

// TestCodexUpRefreshFailureSaysSignedOut: a failed refresh gives the sign-in hint.
func TestCodexUpRefreshFailureSaysSignedOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	c, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Minute).Unix()})
	access := h + "." + base64.RawURLEncoding.EncodeToString(c) + ".sig"
	doc, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt",
		"tokens": map[string]any{"access_token": access, "refresh_token": "rt-dead", "account_id": "acct-1"}})
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid_grant", http.StatusUnauthorized)
	}))
	defer srv.Close()
	oldURL := codexRefreshURL
	codexRefreshURL = srv.URL
	defer func() { codexRefreshURL = oldURL }()

	_, _, err := CodexAuth(context.Background())
	if err == nil || !errors.Is(err, ErrCodexSignedOut) {
		t.Fatalf("err = %v, want the signed-out error", err)
	}
}
