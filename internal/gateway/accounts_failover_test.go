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
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func testJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// writeChatGPTLogin signs the Codex CLI in as email under home.
func writeChatGPTLogin(t *testing.T, home, email, access, refresh string) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": testJWT(map[string]any{"email": email,
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + email}}),
		"access_token": access, "refresh_token": refresh, "account_id": "acct-" + email,
	}}
	b, _ := json.Marshal(auth)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	// Codex lists its models in its own cache; magpie reads that
	cache := `{"models":[{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","priority":1}]}`
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A signed-in account that runs out of quota hands the request to the other
// login the user switched on, with that login's own tokens, and the next
// request goes straight to it.
func TestSignedInAccountFailsOverToOtherLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	exp := time.Now().Add(time.Hour).Unix()
	accessOf := func(who string) string { return testJWT(map[string]any{"exp": exp, "sub": who}) }
	writeChatGPTLogin(t, home, "me@example.com", accessOf("me"), "r-me")

	var mu sync.Mutex
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		mu.Lock()
		seen = append(seen, auth)
		mu.Unlock()
		if auth == "Bearer "+accessOf("me") {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"usage limit reached"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n")
	}))
	defer up.Close()
	oldBase := provider.CodexBase
	provider.CodexBase = up.URL + "/codex"
	t.Cleanup(func() { provider.CodexBase = oldBase })

	// the second login is signed in, which keeps me on and makes the other on too;
	// switching back leaves me current and the other one serving alongside
	if _, err := provider.AddLogin("codex", func() error {
		writeChatGPTLogin(t, home, "other@example.com", accessOf("other"), "r-other")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SwitchLogin("codex", "me@example.com"); err != nil {
		t.Fatal(err)
	}

	srv := New()
	h := srv.Handler()
	body := `{"model":"codex/gpt-5.5","input":"hi","stream":true}`
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"Bearer " + accessOf("me"), "Bearer " + accessOf("other"), "Bearer " + accessOf("other")}
	if len(seen) != len(want) {
		t.Fatalf("upstream saw %d requests, want %d: %v", len(seen), len(want), seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("upstream request %d authorized with %q, want %q", i, seen[i], want[i])
		}
	}
	var noted int
	for _, c := range srv.Recent() {
		if c.Fallback != "" {
			noted++
			if c.Fallback != "codex (me@example.com)" || c.Provider != "codex" {
				t.Fatalf("fallback note = %q, provider %q", c.Fallback, c.Provider)
			}
		}
	}
	if noted != 1 {
		t.Fatalf("calls with a fallback note = %d, want 1", noted)
	}
}
