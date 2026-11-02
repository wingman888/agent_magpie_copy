package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func zzJwt(m map[string]any) string {
	b, _ := json.Marshal(m)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func zzCodexSignIn(t *testing.T, home, email, acct string) {
	t.Helper()
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      zzJwt(map[string]any{"email": email}),
		"access_token":  zzJwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": email}),
		"refresh_token": "r-" + acct, "account_id": acct}})
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Two ChatGPT accounts: the signed-in one runs out of quota, the other,
// switched on, finishes the request with its own credentials and then
// serves the next one straight away.
func TestZZSubscriptionAccountsRelay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	zzCodexSignIn(t, home, "first@example.com", "acct-1")
	if _, err := provider.Logins("codex"); err != nil {
		t.Fatal(err)
	}
	zzCodexSignIn(t, home, "second@example.com", "acct-2")
	if _, err := provider.Logins("codex"); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var hits []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		hits = append(hits, id+":"+r.Header.Get("Authorization")[len("Bearer "):][:2])
		mu.Unlock()
		if id == "acct-2" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			w.Write([]byte(`{"error":{"message":"You have zzHit your usage limit"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.5\"}}\n\n" +
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n" +
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n"))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	s := New()
	call := func() (int, string) {
		return zzSend(s.Handler(), "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","stream":true}`)
	}

	// the other account is not on: the quota error reaches the client
	if code, _ := call(); code != 429 {
		t.Fatalf("with the second account off: %d", code)
	}
	if err := provider.SetLoginOn("codex", "first@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetLoginOn("codex", "second@example.com", false); err == nil {
		t.Fatal("the signed-in account was switched off")
	}

	// let the first request's rest end: it is a different run of the server
	s = New()
	mu.Lock()
	hits = nil
	mu.Unlock()
	code, body := call()
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("relay: %d %s", code, body)
	}
	c := s.Recent()[0]
	if c.Provider != "codex" || !strings.Contains(c.Fallback, "codex") || !strings.Contains(c.Fallback, "second@example.com") {
		t.Fatalf("call: %+v", c)
	}
	if code, _ := call(); code != 200 {
		t.Fatalf("second request: %d", code)
	}
	mu.Lock()
	got := strings.Join(hits, " ")
	mu.Unlock()
	// the signed-in account was asked once; the next request went straight on
	if len(hits) != 3 || !strings.HasPrefix(hits[0], "acct-2") || !strings.HasPrefix(hits[1], "acct-1") || !strings.HasPrefix(hits[2], "acct-1") {
		t.Fatalf("hits: %s", got)
	}
	// each account used its own token
	if hits[0] == hits[1] {
		t.Fatalf("same credentials for both accounts: %s", got)
	}
	// the agent's own store still belongs to the signed-in account
	b, _ := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if !strings.Contains(string(b), "acct-2") || strings.Contains(string(b), "acct-1") {
		t.Fatalf("auth.json: %s", b)
	}
}
