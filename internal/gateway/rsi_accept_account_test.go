package gateway_test

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

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

func jwt(m map[string]any) string {
	b, _ := json.Marshal(m)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func signIn(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		jwt(map[string]any{"email": "me@example.com"}), jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)
	return home
}

type backend struct {
	mu   sync.Mutex
	path string
	head http.Header
	got  []byte
}

func (b *backend) rec(r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.path, b.head = r.URL.Path, r.Header.Clone()
	b.got, _ = io.ReadAll(r.Body)
}

func post(t *testing.T, path, body string) (int, string, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	gateway.New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String(), rec.Header()
}

func codexBackend(t *testing.T, b *backend, withType bool) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.rec(r)
		if withType {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header()["Content-Type"] = nil
		}
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-5.5\"}}\n\n"+
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n"+
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n")
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

func TestAcceptCodexNonStreamingViaGateway(t *testing.T) {
	signIn(t)
	b := &backend{}
	codexBackend(t, b, false)
	code, body, _ := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","max_output_tokens":20,"temperature":0.3,"reasoning":{"effort":"low"}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.HasSuffix(b.path, "/responses") || b.head.Get("chatgpt-account-id") != "acct-1" || !strings.HasPrefix(b.head.Get("Authorization"), "Bearer h.") {
		t.Fatalf("upstream call: %s %v", b.path, b.head)
	}
	var up map[string]any
	json.Unmarshal(b.got, &up)
	if _, ok := up["max_output_tokens"]; ok || up["temperature"] != nil || up["stream"] != true || up["store"] != false || up["model"] != "gpt-5.5" || up["reasoning"] == nil {
		t.Fatalf("upstream body: %s", b.got)
	}
	in, ok := up["input"].([]any)
	if !ok || len(in) == 0 || !strings.Contains(string(b.got), "ping") {
		t.Fatalf("input: %s", b.got)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(body), &res); err != nil || res["object"] != "response" || !strings.Contains(body, "pong") {
		t.Fatalf("reply: %s", body)
	}
	if !strings.Contains(body, "usage") {
		t.Fatalf("no usage: %s", body)
	}
}

func TestAcceptCodexStreamingViaGateway(t *testing.T) {
	for _, withType := range []bool{true, false} {
		signIn(t)
		b := &backend{}
		codexBackend(t, b, withType)
		code, body, h := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","stream":true,"max_output_tokens":20}`)
		if code != 200 || !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "response.completed") {
			t.Fatalf("stream(%v): %d %s", withType, code, body)
		}
		if !strings.Contains(h.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("content-type(%v): %q", withType, h.Get("Content-Type"))
		}
		var up map[string]any
		json.Unmarshal(b.got, &up)
		if _, ok := up["max_output_tokens"]; ok || up["store"] != false || up["model"] != "gpt-5.5" {
			t.Fatalf("relayed body: %s", b.got)
		}
		if _, isList := up["input"].([]any); !isList {
			t.Fatalf("input not a list: %s", b.got)
		}
	}
}

func TestAcceptSignedOutCodexIsClearError(t *testing.T) {
	home := signIn(t)
	os.Remove(filepath.Join(home, ".codex", "auth.json"))
	code, body, _ := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if code < 400 || body == "" {
		t.Fatalf("expected an error, got %d %q", code, body)
	}
}

func TestAcceptCopilotChatViaGateway(t *testing.T) {
	home := signIn(t)
	os.MkdirAll(filepath.Join(home, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "github-copilot", "apps.json"), []byte(`{"github.com:x":{"user":"octocat","oauth_token":"gho_x"}}`), 0o600)
	b := &backend{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.rec(r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"c1","object":"chat.completion","model":"claude-sonnet-4.5","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = tokens.URL
	defer func() { provider.CopilotTokenURL = old }()

	code, body, _ := post(t, "/v1/chat/completions", `{"model":"copilot/claude-sonnet-4.5","messages":[{"role":"user","content":"ping"}]}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.HasSuffix(b.path, "/chat/completions") || b.head.Get("Authorization") != "Bearer sess" || b.head.Get("X-Initiator") != "user" {
		t.Fatalf("upstream: %s %v", b.path, b.head)
	}
	var up map[string]any
	json.Unmarshal(b.got, &up)
	if up["model"] != "claude-sonnet-4.5" {
		t.Fatalf("upstream body: %s", b.got)
	}
}
