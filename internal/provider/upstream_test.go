package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCodexFile(t *testing.T, home string, accessExp int64) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"custom":         "keep-me",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "dev@example.com"}),
			"access_token":  fakeJWT(map[string]any{"exp": accessExp}),
			"refresh_token": "rt-old",
			"account_id":    "acct-1",
		},
	})
	p := filepath.Join(home, ".codex", "auth.json")
	writeTemp(t, p, string(body))
	return p
}

func TestCodexFreshTokenIsNotRefreshed(t *testing.T) {
	home := isolate(t)
	writeCodexFile(t, home, time.Now().Add(time.Hour).Unix())
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()

	tok, acct, err := CodexAccess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hit || tok == "" || acct != "acct-1" {
		t.Errorf("hit=%v tok=%q acct=%q", hit, tok, acct)
	}
}

func TestCodexExpiringTokenIsRefreshedAndWrittenBack(t *testing.T) {
	home := isolate(t)
	path := writeCodexFile(t, home, time.Now().Add(time.Minute).Unix())
	newAccess := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	var sent map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&sent)
		json.NewEncoder(w).Encode(map[string]string{"access_token": newAccess, "refresh_token": "rt-new"})
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()

	tok, _, err := CodexAccess(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != newAccess {
		t.Errorf("returned token is not the refreshed one")
	}
	if sent["grant_type"] != "refresh_token" || sent["refresh_token"] != "rt-old" || sent["client_id"] != codexClientID {
		t.Errorf("refresh request: %v", sent)
	}
	b, _ := os.ReadFile(path)
	var doc map[string]any
	json.Unmarshal(b, &doc)
	toks := doc["tokens"].(map[string]any)
	if toks["access_token"] != newAccess || toks["refresh_token"] != "rt-new" {
		t.Errorf("tokens not written back: %v", toks)
	}
	if toks["id_token"] == nil || toks["account_id"] != "acct-1" {
		t.Errorf("token fields lost: %v", toks)
	}
	if doc["custom"] != "keep-me" || doc["auth_mode"] != "chatgpt" {
		t.Errorf("other fields lost: %v", doc)
	}
	if _, ok := doc["last_refresh"]; !ok {
		t.Errorf("last_refresh not set")
	}
}

func TestCodexRefreshFailureSaysSignOut(t *testing.T) {
	home := isolate(t)
	writeCodexFile(t, home, time.Now().Add(time.Minute).Unix())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusBadRequest)
	}))
	defer srv.Close()
	old := CodexRefreshURL
	CodexRefreshURL = srv.URL
	defer func() { CodexRefreshURL = old }()

	_, _, err := CodexAccess(context.Background())
	if err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("err = %v, want a sign-in instruction", err)
	}
}

func TestCopilotRejectedSessionAsksForSignIn(t *testing.T) {
	isolate(t)
	writeCopilotLogin(t, os.Getenv("HOME"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	old := CopilotTokenURL
	CopilotTokenURL = srv.URL
	defer func() { CopilotTokenURL = old }()

	_, _, err := CopilotSession(context.Background())
	if err == nil || !strings.Contains(err.Error(), "请重新登录 Copilot") {
		t.Fatalf("err = %v", err)
	}
}
