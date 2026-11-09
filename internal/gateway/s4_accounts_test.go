package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// s4Upstream is the ChatGPT backend: it answers by the bearer token a request
// carries, and a token in limited gets 429.
type s4Upstream struct {
	mu      sync.Mutex
	seen    []string
	limited map[string]bool
}

const s4Reply = "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"object\":\"response\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"

func (u *s4Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.seen = append(u.seen, tok)
	limited := u.limited[tok]
	u.mu.Unlock()
	if limited {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, s4Reply)
}

func (u *s4Upstream) take() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := u.seen
	u.seen = nil
	return out
}

// s4CodexAuth is a ChatGPT sign-in file for email whose access token is tok.
func s4CodexAuth(t *testing.T, path, email, tok string) {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	b, _ := json.Marshal(map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": "2026-01-01T00:00:00Z",
		"tokens": map[string]any{
			"id_token":      "h." + base64.RawURLEncoding.EncodeToString(claims) + ".sig",
			"access_token":  tok,
			"refresh_token": "r-" + tok,
			"account_id":    "acct-" + tok,
		},
	})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestS4LoginQuotaMovesToTheOtherLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	auth := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	models, _ := json.Marshal(map[string]any{"models": []any{map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1}}})
	if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), models, 0o600); err != nil {
		t.Fatal(err)
	}

	u := &s4Upstream{limited: map[string]bool{"tok-b": true}}
	up := httptest.NewServer(u)
	t.Cleanup(up.Close)
	oldBase := provider.CodexBase
	provider.CodexBase = up.URL
	t.Cleanup(func() { provider.CodexBase = oldBase })

	// a is signed in to; magpie saves it, then signs in to b through `codex
	// login`, which a fake binary on PATH stands in for
	s4CodexAuth(t, auth, "a@example.com", "tok-a")
	s4CodexAuth(t, filepath.Join(home, "next-auth.json"), "b@example.com", "tok-b")
	bin := t.TempDir()
	script := "#!/bin/sh\ncp \"$HOME/next-auth.json\" \"$HOME/.codex/auth.json\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if user, err := provider.AddLogin("codex"); err != nil || user != "b@example.com" {
		t.Fatalf("AddLogin = %q, %v", user, err)
	}
	if err := provider.SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	const body = `{"model":"gpt-5.5","stream":true,"input":"hi"}`
	s := New()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(u.take(), "|"); got != "tok-b|tok-a" {
		t.Errorf("upstream saw %s", got)
	}
	c := s.Recent()[0]
	if c.Status != 200 || c.Provider != "codex" || c.Fallback != "codex (b@example.com)" {
		t.Errorf("call: status %d provider %q fallback %q", c.Status, c.Provider, c.Fallback)
	}

	// the login that was limited rests; the next request goes to a directly
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("second status %d: %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(u.take(), "|"); got != "tok-a" {
		t.Errorf("second request saw %s", got)
	}
	if c := s.Recent()[0]; c.Fallback != "" {
		t.Errorf("second call should not fall back: %q", c.Fallback)
	}
}
