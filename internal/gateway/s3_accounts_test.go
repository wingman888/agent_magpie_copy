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

// s3GatewayEnv keeps config, cache, home and Claude's directory out of the real ones.
func s3GatewayEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	return dir
}

// s3Token is an unsigned JWT whose "name" claim is name, valid for years.
func s3Token(name string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(map[string]any{"name": name, "exp": int64(4102444800)})
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// s3TokenName reads the name claim back out of a token the test made.
func s3TokenName(tok string) string {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	name, _ := m["name"].(string)
	return name
}

// s3SignIn writes Codex CLI's auth file as if signed in as email.
func s3SignIn(t *testing.T, home, email, name string) {
	t.Helper()
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      s3IDToken(email),
			"access_token":  s3Token(name),
			"refresh_token": "refresh-" + name,
			"account_id":    "acct-" + name,
		},
	}
	b, _ := json.MarshalIndent(body, "", "  ")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// s3IDToken is an id token carrying the account's email.
func s3IDToken(email string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(map[string]any{"email": email, "exp": int64(4102444800),
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestS3AccountFailsOverToTheOnAccount(t *testing.T) {
	dir := s3GatewayEnv(t)
	home := filepath.Join(dir, "home")
	s3SignIn(t, home, "a@example.com", "a")
	provider.Remembered("codex") // magpie remembers a
	// b signs in through magpie's own sign-in, which runs codex from PATH; a
	// stand-in codex writes b's sign-in where the real one would
	bHome := filepath.Join(dir, "b-home")
	s3SignIn(t, bHome, "b@example.com", "b")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stand := "#!/bin/sh\ncp \"" + filepath.Join(bHome, ".codex", "auth.json") + "\" \"$HOME/.codex/auth.json\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(stand), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if user, err := provider.AddAccount("codex"); err != nil || !strings.EqualFold(user, "b@example.com") {
		t.Fatalf("AddAccount = %q, %v", user, err)
	}
	// a is kept with its own sign-in; it serves alongside b
	if err := provider.SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		name := s3TokenName(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Lock()
		seen = append(seen, name)
		mu.Unlock()
		if name == "b" {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"usage limit reached"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.5\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	t.Cleanup(up.Close)
	oldBase := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = oldBase })

	s := New()
	code, body := s3Post(t, s, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	c := s.Recent()[0]
	if c.Provider != "codex" || !strings.Contains(c.Fallback, "codex") || c.Key != "a@example.com" {
		t.Fatalf("call record: provider %q fallback %q key %q", c.Provider, c.Fallback, c.Key)
	}

	// the next request goes straight to the account that served
	if code, body := s3Post(t, s, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`); code != 200 {
		t.Fatalf("second request: %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(seen, ","); got != "b,a,a" {
		t.Fatalf("accounts tried = %s, want b (exhausted), a, then a straight away", got)
	}
}

// s3Post sends one request through the gateway's own handler.
func s3Post(t *testing.T, s *Server, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}
