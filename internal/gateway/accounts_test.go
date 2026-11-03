package gateway

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// signInCodex writes a ChatGPT sign-in into the agent's auth.json and returns
// its access token.
func signInCodex(t *testing.T, home, email, acct string) string {
	t.Helper()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	access := claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": email})
	id := claims(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": acct}})
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":%q}}`,
		id, access, acct)
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

// A signed-in ChatGPT account whose current login runs out of quota hands the
// request to the other account that is on, with that account's own token, and
// the next request goes straight to it.
func TestSignedInAccountFailsOverToTheOtherOnAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	accessA := signInCodex(t, home, "a@example.com", "acct-a")
	provider.Logins("codex")
	accessB := signInCodex(t, home, "b@example.com", "acct-b")
	provider.Logins("codex") // B is remembered, and on by default
	signInCodex(t, home, "a@example.com", "acct-a")
	provider.Logins("codex") // A is the agent's login again

	var mu sync.Mutex
	var auths []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "Bearer "+accessA {
			w.Header().Set("Content-Type", jsonType)
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"usage limit reached"}}`)
			return
		}
		w.Header().Set("Content-Type", streamType)
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	s := New()
	code, body := callGateway(t, s.Handler(), "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","stream":true}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	mu.Lock()
	first := append([]string(nil), auths...)
	mu.Unlock()
	if len(first) != 2 || first[0] != "Bearer "+accessA || first[1] != "Bearer "+accessB {
		t.Fatalf("upstream auth order: %q", first)
	}
	if rec := s.Recent()[0]; rec.Status != 200 || rec.Provider != "codex" || !strings.Contains(rec.Fallback, "a@example.com") {
		t.Fatalf("record: status %d provider %q fallback %q", rec.Status, rec.Provider, rec.Fallback)
	}

	// the account that failed rests for a minute: the next request goes to B alone
	code, body = callGateway(t, s.Handler(), "/v1/responses", `{"model":"codex/gpt-5.5","input":"again","stream":true}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("second status %d: %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 3 || auths[2] != "Bearer "+accessB {
		t.Fatalf("second request auth order: %q", auths)
	}
}
