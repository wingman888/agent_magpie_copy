package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// An Anthropic client asks for a Codex account model. The request is translated
// to the Responses API, the backend's reply comes without Content-Type, and the
// backend's rules are applied to the body.
func TestAnthropicClientReachesCodexAccount(t *testing.T) {
	f := &codexUp{reply: codexReply, noCType: true}
	setupCodex(t, f, future())

	code, body := post(t, "/v1/messages", `{"model":"codex/gpt-5.5","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "Hello") {
		t.Errorf("reply lacks the generated text: %s", body)
	}
	if f.got["stream"] != true {
		t.Errorf("backend got stream=%v, want true", f.got["stream"])
	}
	if _, ok := f.got["max_output_tokens"]; ok {
		t.Errorf("backend got max_output_tokens: %v", f.got["max_output_tokens"])
	}
	if _, ok := f.got["input"].([]any); !ok {
		t.Errorf("backend input is not a list: %v", f.got["input"])
	}
	if f.got["model"] != "gpt-5.5" {
		t.Errorf("backend model = %v, want gpt-5.5", f.got["model"])
	}
	if f.head.Get("chatgpt-account-id") != "acct-123" || f.head.Get("Authorization") == "" {
		t.Errorf("backend headers lack the account: %v", f.head)
	}
}

// A Chat Completions client asks for a Copilot account model. The request goes
// to the session token's endpoint, labelled as a user turn.
func TestChatClientReachesCopilotAccount(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	var srv *httptest.Server
	var gotPath string
	var gotHead http.Header
	var gotBody map[string]any
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "sess-gw",
				"expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints":  map[string]any{"api": srv.URL + "/api"},
			})
		case "/api/chat/completions":
			gotPath, gotHead = r.URL.Path, r.Header.Clone()
			b, _ := io.ReadAll(r.Body)
			gotBody = nil
			json.Unmarshal(b, &gotBody)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"c1","object":"chat.completion","model":"claude-sonnet-4.5",`+
				`"choices":[{"index":0,"message":{"role":"assistant","content":"Hi there"},"finish_reason":"stop"}],`+
				`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldURL := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = oldURL })

	if err := os.MkdirAll(filepath.Join(cfg, "github-copilot"), 0o700); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:x":{"user":"octocat","oauth_token":"gho_gw"}}`
	if err := os.WriteFile(filepath.Join(cfg, "github-copilot", "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}

	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "Hi there") {
		t.Errorf("reply lacks the generated text: %s", body)
	}
	if gotPath != "/api/chat/completions" {
		t.Errorf("request went to %q, want the session endpoint", gotPath)
	}
	if gotHead.Get("Authorization") != "Bearer sess-gw" {
		t.Errorf("Authorization = %q", gotHead.Get("Authorization"))
	}
	if gotHead.Get("X-Initiator") != "user" {
		t.Errorf("X-Initiator = %q, want user", gotHead.Get("X-Initiator"))
	}
	if gotBody["model"] != "claude-sonnet-4.5" {
		t.Errorf("backend model = %v, want the unprefixed name", gotBody["model"])
	}
}
