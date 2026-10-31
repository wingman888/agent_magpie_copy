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

func TestCodexSideAccountTakesOverOnQuota(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	jwt := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	login := func(email string) {
		auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token": jwt(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{
				"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + email}}),
			"access_token":  jwt(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "sub": email}),
			"refresh_token": "r-" + email, "account_id": "acct-" + email}}
		b, _ := json.Marshal(auth)
		os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
		os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600)
	}
	login("a@example.com")
	provider.Logins("codex")
	login("b@example.com") // b is current; a is remembered
	if _, err := provider.Logins("codex"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetLoginOn("codex", "b@example.com", false); err == nil {
		t.Fatal("turning off the current login must fail")
	}
	if err := provider.SetLoginOn("codex", "a@example.com", true); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var accts []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		acct := r.Header.Get("chatgpt-account-id")
		mu.Lock()
		accts = append(accts, acct)
		mu.Unlock()
		if acct == "acct-b@example.com" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":1,"output_tokens":1}}}`))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	for i := 0; i < 2; i++ {
		code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
		if code != 200 || !strings.Contains(body, "pong") {
			t.Fatalf("request %d: %d %s", i, code, body)
		}
	}
	got := strings.Join(accts, ",")
	want := "acct-b@example.com,acct-a@example.com,acct-a@example.com"
	if got != want {
		t.Fatalf("upstream accounts %s, want %s", got, want)
	}
	_ = fmt.Sprint
}
