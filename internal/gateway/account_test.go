package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
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

// codexFake is a fake Codex backend. It sends no Content-Type, the way the
// real one may.
type codexFake struct {
	got    []byte
	path   string
	head   http.Header
	stream bool
}

func (f *codexFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.got, _ = io.ReadAll(r.Body)
	f.path, f.head = r.URL.Path, r.Header
	// an explicit nil keeps net/http from sniffing a Content-Type
	w.Header()["Content-Type"] = nil
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n")
	fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n")
	fmt.Fprint(w, `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":3,"output_tokens":2,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}

`)
}

func setupCodex(t *testing.T) *codexFake {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	exp := time.Now().Add(time.Hour).Unix()
	acc := jwtFor(map[string]any{"exp": exp})
	body := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"rt","account_id":"acct-1"}}`,
		jwtFor(map[string]any{"email": "dev@example.com"}), acc)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &codexFake{}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL
	t.Cleanup(func() { provider.CodexBase = old })
	return f
}

func jwtFor(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	enc := func(x []byte) string { return base64.RawURLEncoding.EncodeToString(x) }
	return enc([]byte(`{"alg":"none"}`)) + "." + enc(b) + ".sig"
}

func TestCodexNonStreamResponsesIsCollected(t *testing.T) {
	f := setupCodex(t)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":16,"temperature":0.2}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, `"hello"`) || !strings.Contains(body, `"resp_1"`) || strings.Contains(body, "response.created") {
		t.Errorf("not the final response object: %s", body)
	}
	var sent map[string]any
	json.Unmarshal(f.got, &sent)
	if sent["stream"] != true || sent["store"] != false || sent["model"] != "gpt-5.5" {
		t.Errorf("upstream body %s", f.got)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("%s reached the Codex backend", k)
		}
	}
	if items, ok := sent["input"].([]any); !ok || len(items) != 1 {
		t.Errorf("input not turned into a list: %v", sent["input"])
	}
	if f.head.Get("Authorization") == "" || f.head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("headers %v", f.head)
	}
	if f.path != "/responses" {
		t.Errorf("path %s", f.path)
	}
}

func TestCodexStreamResponsesIsForwarded(t *testing.T) {
	f := setupCodex(t)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":"hi","top_p":0.5}`)
	if code != 200 || !strings.Contains(body, "response.completed") {
		t.Fatalf("%d %s", code, body)
	}
	if !bytes.Contains(f.got, []byte(`"stream":true`)) || bytes.Contains(f.got, []byte("top_p")) {
		t.Errorf("upstream body %s", f.got)
	}
}

func TestCodexChatClientIsTranslated(t *testing.T) {
	f := setupCodex(t)
	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"temperature":0.3}`)
	if code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("%d %s", code, body)
	}
	if bytes.Contains(f.got, []byte("max_output_tokens")) || bytes.Contains(f.got, []byte("temperature")) {
		t.Errorf("refused field sent: %s", f.got)
	}
	if !bytes.Contains(f.got, []byte(`"stream":true`)) {
		t.Errorf("upstream not streamed: %s", f.got)
	}
}

func TestCopilotToolTurnIsAgentInitiated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	apps := filepath.Join(home, "xdg", "github-copilot")
	os.MkdirAll(apps, 0o755)
	os.WriteFile(filepath.Join(apps, "apps.json"), []byte(`{"github.com:x":{"user":"octo","oauth_token":"gho_x"}}`), 0o600)

	var chatPath string
	var head http.Header
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fmt.Fprintf(w, `{"token":"sess-9","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), srv.URL+"/api")
		default:
			chatPath, head = r.URL.Path, r.Header
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"c1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
		}
	}))
	defer srv.Close()
	oldTok := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/token"
	t.Cleanup(func() { provider.CopilotTokenURL = oldTok })

	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"t1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"t1","content":"ok"}]}`)
	if code != 200 || !strings.Contains(body, "done") {
		t.Fatalf("%d %s", code, body)
	}
	if chatPath != "/api/chat/completions" {
		t.Errorf("path %s, want the session API", chatPath)
	}
	if head.Get("Authorization") != "Bearer sess-9" || head.Get("X-Initiator") != "agent" {
		t.Errorf("headers %v", head)
	}
}
