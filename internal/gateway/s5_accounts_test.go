package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// s5Reply is a Responses stream that answers "pong".
const s5Reply = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.5\"}}\n\n" +
	"data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n"

// s5Codex is the ChatGPT backend for two signed-in accounts: the first is
// rate limited, the second answers. It records the bearer token of every request.
type s5Codex struct {
	mu       sync.Mutex
	tokens   []string
	accounts []string
}

func (c *s5Codex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	io.ReadAll(r.Body)
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	c.mu.Lock()
	c.tokens = append(c.tokens, tok)
	c.accounts = append(c.accounts, r.Header.Get("chatgpt-account-id"))
	c.mu.Unlock()
	if tok == "tok-a" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, s5Reply)
}

func (c *s5Codex) seen() ([]string, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.tokens...), append([]string(nil), c.accounts...)
}

// s5JWT is a token whose payload is the given claims; nobody checks the signature.
func s5JWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

// s5Accounts sets up a signed-in Codex account a@example.com and a remembered,
// switched-on account b@example.com, and points the backend at up.
func s5Accounts(t *testing.T, up *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     s5JWT(map[string]any{"email": "a@example.com"}),
		"access_token": "tok-a", "refresh_token": "rA", "account_id": "acct-a"}}
	writeS5JSON(t, filepath.Join(home, ".codex", "auth.json"), auth)

	// magpie's accounts file sits beside providers.json
	accounts := map[string]any{"accounts": []any{map[string]any{
		"agent": "codex", "user": "b@example.com", "on": true,
		"creds": map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":     s5JWT(map[string]any{"email": "b@example.com"}),
			"access_token": "tok-b", "refresh_token": "rB", "account_id": "acct-b"}},
	}}}
	writeS5JSON(t, filepath.Join(filepath.Dir(provider.Path()), "accounts", "accounts.json"), accounts)
}

func writeS5JSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func s5Post(t *testing.T, h http.Handler, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

func TestS5CodexAccountFallsOverToTheNextOnAccount(t *testing.T) {
	up := &s5Codex{}
	backend := httptest.NewServer(up)
	defer backend.Close()
	s5Accounts(t, backend)

	srv := New()
	const req = `{"model":"codex/gpt-5.5","input":"ping","stream":true}`
	code, body := s5Post(t, srv.Handler(), req)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	tokens, _ := up.seen()
	if !reflect.DeepEqual(tokens, []string{"tok-a", "tok-b"}) {
		t.Fatalf("upstream tokens %v, want the signed-in account then the remembered one", tokens)
	}
	calls := srv.Recent()
	if calls[0].Provider != "codex" || calls[0].Status != 200 {
		t.Fatalf("call record: %+v", calls[0])
	}
	if !strings.Contains(calls[0].Fallback, "a@example.com") {
		t.Fatalf("fallback %q does not name the account that failed", calls[0].Fallback)
	}
	if calls[0].Key != "b@example.com" {
		t.Fatalf("call key %q, want the account that answered", calls[0].Key)
	}

	// the account that just failed rests, so the next request goes straight to the other
	if code, body := s5Post(t, srv.Handler(), req); code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("second status %d: %s", code, body)
	}
	tokens, accounts := up.seen()
	if !reflect.DeepEqual(tokens, []string{"tok-a", "tok-b", "tok-b"}) {
		t.Fatalf("second request tokens %v", tokens)
	}
	if accounts[2] != "acct-b" {
		t.Fatalf("second request signed with account %q, want acct-b", accounts[2])
	}
	if calls := srv.Recent(); calls[0].Fallback != "" || calls[0].Provider != "codex" {
		t.Fatalf("second call record: %+v", calls[0])
	}
}
