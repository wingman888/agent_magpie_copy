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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// accountFake answers as the ChatGPT backend does for two accounts: the
// first is out of allowance (429), the second answers. It records which
// account each request was sent with.
type accountFake struct {
	mu    sync.Mutex
	calls []string
}

func (a *accountFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("chatgpt-account-id")
	a.mu.Lock()
	a.calls = append(a.calls, id)
	a.mu.Unlock()
	if id == "acct-1" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
		`data: {"type":"response.output_text.delta","delta":"pong"}`,
		`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
}

func (a *accountFake) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// A request the signed-in ChatGPT account cannot serve goes to the other
// switched-on account with its own tokens; the next request goes there first.
func TestCodexAccountsFallBackToTheOnAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	exp := time.Now().Add(time.Hour).Unix()
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		claims(map[string]any{"email": "me@example.com"}), claims(map[string]any{"exp": exp}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)
	os.MkdirAll(filepath.Join(home, ".config", "magpie"), 0o755)
	accounts := fmt.Sprintf(`{"logins":[{"agent":"codex","user":"other@example.com","plan":"plus","on":true,"login":{"id_token":%q,"access_token":%q,"refresh_token":"r2","account_id":"acct-2"}}]}`,
		claims(map[string]any{"email": "other@example.com"}), claims(map[string]any{"exp": exp}))
	os.WriteFile(filepath.Join(home, ".config", "magpie", "accounts.json"), []byte(accounts), 0o600)

	up := &accountFake{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	old := provider.CodexBase
	provider.CodexBase = srv.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	s := New()
	send := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses",
			strings.NewReader(`{"model":"codex/gpt-5.5","input":"ping"}`)))
		return rec
	}

	rec := send()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := up.sent(); !reflect.DeepEqual(got, []string{"acct-1", "acct-2"}) {
		t.Fatalf("upstream saw %v, want the signed-in account then the other", got)
	}
	var fallback, served string
	for _, c := range s.Recent() {
		if c.Fallback != "" {
			fallback, served = c.Fallback, c.Provider
		}
	}
	if fallback != "codex (account me@example.com)" || served != "codex" {
		t.Fatalf("call record: fallback %q provider %q", fallback, served)
	}

	rec = send()
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") {
		t.Fatalf("second status %d: %s", rec.Code, rec.Body.String())
	}
	if got := up.sent(); !reflect.DeepEqual(got, []string{"acct-1", "acct-2", "acct-2"}) {
		t.Fatalf("second request saw %v, want the other account straight away", got)
	}
}
