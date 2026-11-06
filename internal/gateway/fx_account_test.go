package gateway

import (
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

	"github.com/yetone/dial/internal/provider"
)

// fxUpstream records the last request a stand-in backend received.
type fxUpstream struct {
	mu        sync.Mutex
	path      string
	auth      string
	integ     string
	initiator string
	model     string
	seen      bool
}

// fxCopilotLogin writes a Copilot login under a fresh config dir and points
// the token exchange at a stand-in that also serves chat as SSE.
func fxCopilotLogin(t *testing.T, up *fxUpstream) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	dir := filepath.Join(home, "xdg", "github-copilot")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "apps.json"),
		[]byte(`{"github.com:Iv1.fx":{"user":"fx-octo","oauth_token":"gho_fx_oauth"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			b, _ := json.Marshal(map[string]any{
				"token":      "tid=fx-gw-session",
				"expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints":  map[string]string{"api": srv.URL},
			})
			w.Write(b)
		case "/chat/completions":
			var in struct {
				Model string `json:"model"`
			}
			json.Unmarshal(body, &in)
			up.mu.Lock()
			up.seen, up.path, up.auth = true, r.URL.Path, r.Header.Get("Authorization")
			up.integ, up.initiator, up.model = r.Header.Get("Copilot-Integration-Id"), r.Header.Get("X-Initiator"), in.Model
			up.mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"fx-ok\"},\"finish_reason\":null}]}\n\n"+
				"data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"gpt-5.5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	orig := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = orig })
}

func TestFxCopilotLoginIsRoutedWithSession(t *testing.T) {
	var up fxUpstream
	fxCopilotLogin(t, &up)

	body := `{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("copilot should be routed, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fx-ok") {
		t.Fatalf("client should get the reply text, got %s", rec.Body.String())
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if !up.seen {
		t.Fatal("upstream was never called")
	}
	if up.path != "/chat/completions" || up.auth != "Bearer tid=fx-gw-session" {
		t.Fatalf("upstream not signed with the session token: path=%q auth=%q", up.path, up.auth)
	}
	if up.integ == "" {
		t.Fatal("upstream must get a non-empty Copilot-Integration-Id header")
	}
	if up.initiator != "user" {
		t.Fatalf("last message is from the user, want X-Initiator user, got %q", up.initiator)
	}
	if up.model != "gpt-5.5" {
		t.Fatalf("model should be sent without its account prefix, got %q", up.model)
	}
}

func TestFxCopilotLoginSignedOutIsAnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	dir := filepath.Join(home, "xdg", "github-copilot")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "apps.json"), []byte(`{"github.com:Iv1.fx":{"user":"fx-octo","oauth_token":"gho_fx"}}`), 0o600)
	// a login with a token that the exchange rejects
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	}))
	defer srv.Close()
	orig := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/token"
	defer func() { provider.CopilotTokenURL = orig }()

	body := `{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code == http.StatusOK {
		t.Fatalf("a rejected login must not succeed: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sign in") {
		t.Fatalf("error should ask the user to sign in again, got %s", rec.Body.String())
	}
}
