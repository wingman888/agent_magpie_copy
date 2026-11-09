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
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// lgnxUpstream is the Codex backend. A request carrying the limited account's
// token gets a rate-limit reply; every other token gets a streamed answer.
// It records the token of each request, in order.
type lgnxUpstream struct {
	mu      sync.Mutex
	limited string
	hits    []string
}

func (u *lgnxUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	u.mu.Lock()
	u.hits = append(u.hits, tok)
	u.mu.Unlock()
	if tok == u.limited {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limit reached"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
		`data: {"type":"response.output_text.delta","delta":"pong"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
}

// lgnxAuth is a ChatGPT sign-in as Codex CLI writes it; its access token is
// unexpired, so nothing tries to refresh it.
func lgnxAuth(email, refresh string) (map[string]any, string) {
	access := "h." + lgnxB64(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": email}) + ".s"
	return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": "h." + lgnxB64(map[string]any{"email": email,
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + email}}) + ".s",
		"access_token": access, "refresh_token": refresh, "account_id": "acct-" + email,
	}}, access
}

func lgnxB64(m map[string]any) string {
	b, _ := json.Marshal(m)
	return base64.RawURLEncoding.EncodeToString(b)
}

func lgnxWrite(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A second ChatGPT account switched on takes over when the current one is
// rate limited, with its own token, and stays the choice until the cooldown
// on the first one has passed.
func TestLgnxSecondCodexAccountTakesOverWhenFirstIsLimited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	authPath := filepath.Join(home, ".codex", "auth.json")

	bAuth, bTok := lgnxAuth("b@example.com", "rb")
	aAuth, aTok := lgnxAuth("a@example.com", "ra")
	lgnxWrite(t, authPath, bAuth)
	provider.All() // remembers b as the signed-in account
	// signing in as a keeps b's tokens in magpie's store, to serve beside a
	if _, err := provider.AddLogin("codex", func() error {
		lgnxWrite(t, authPath, aAuth)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetLoginOn("codex", "b@example.com", true); err != nil {
		t.Fatal(err)
	}

	up := &lgnxUpstream{limited: aTok}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	s := New()
	body := `{"model":"codex/gpt-5.5","input":"ping","max_output_tokens":20}`
	if code, out := keyxPost(s, "/v1/responses", body); code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("first request: %d %s", code, out)
	}
	first := s.Recent()[0]
	if first.Status != 200 || first.Provider != "codex" || first.Fallback != "codex (a@example.com)" {
		t.Fatalf("first call record: %+v", first)
	}

	if code, out := keyxPost(s, "/v1/responses", body); code != 200 || !strings.Contains(out, "pong") {
		t.Fatalf("second request: %d %s", code, out)
	}
	if last := s.Recent()[0]; last.Fallback != "" {
		t.Fatalf("second request should go straight to b: %+v", last)
	}
	if want := []string{aTok, bTok, bTok}; !reflect.DeepEqual(up.hits, want) {
		t.Fatalf("upstream saw tokens %q, want %q", up.hits, want)
	}
}
