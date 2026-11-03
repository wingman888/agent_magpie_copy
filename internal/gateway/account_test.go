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
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// agentUp is an upstream reached through an agent's login. It records what it
// was sent and replies with a fixed body. An empty ctype means the reply goes
// out with no Content-Type at all.
type agentUp struct {
	path  string
	head  http.Header
	body  []byte
	ctype string
	reply string
	code  int
}

func (f *agentUp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.body, _ = io.ReadAll(r.Body)
	f.path, f.head = r.URL.Path, r.Header.Clone()
	if f.ctype == "" {
		w.Header()["Content-Type"] = nil // a nil entry stops Go from sniffing a type
	} else {
		w.Header().Set("Content-Type", f.ctype)
	}
	if f.code != 0 {
		w.WriteHeader(f.code)
	}
	io.WriteString(w, f.reply)
}

// jwtFor builds an unsigned JWT carrying the claims.
func jwtFor(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// signInCodex writes a ChatGPT login for Codex into a temp HOME and points the
// Codex backend at f. It returns the access token the login carries.
func signInCodex(t *testing.T, f *agentUp) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	oldBase := provider.CodexBase
	provider.CodexBase = up.URL + "/codex"
	t.Cleanup(func() { provider.CodexBase = oldBase })

	access := jwtFor(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwtFor(map[string]any{"email": "me@example.com"}),
			"access_token":  access,
			"refresh_token": "rt",
			"account_id":    "acct-1",
		},
	})
	home, _ := os.UserHomeDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return access
}

// signInCopilot writes a GitHub Copilot login and points the session exchange
// at up, which must serve /token.
func signInCopilot(t *testing.T, up *httptest.Server) {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	dir := filepath.Join(cfg, "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:Iv1.abc":{"user":"octocat","oauth_token":"gho_test"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
	oldURL := provider.CopilotTokenURL
	provider.CopilotTokenURL = up.URL + "/token"
	t.Cleanup(func() { provider.CopilotTokenURL = oldURL })
}

// codexReply is a Responses stream that says "pong" and ends with the response.
var codexReply = sse(
	`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}`,
	`data: {"type":"response.output_text.delta","delta":"pong"}`,
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}],"usage":{"input_tokens":7,"output_tokens":2}}}`,
)

// checkCodexUpstream checks what the Codex backend was sent.
func checkCodexUpstream(t *testing.T, f *agentUp, access string) {
	t.Helper()
	if f.path != "/codex/responses" {
		t.Errorf("upstream path %s", f.path)
	}
	if got := f.head.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("authorization %q", got)
	}
	if got := f.head.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id %q", got)
	}
	var up map[string]any
	if err := json.Unmarshal(f.body, &up); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if up["model"] != "gpt-5.5" || up["stream"] != true || up["store"] != false {
		t.Errorf("model/stream/store = %v/%v/%v", up["model"], up["stream"], up["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := up[k]; ok {
			t.Errorf("%s sent upstream", k)
		}
	}
	if _, ok := up["input"].([]any); !ok {
		t.Errorf("input is not a list: %v", up["input"])
	}
}

func TestCodexNonStreamingResponsesIsAggregated(t *testing.T) {
	f := &agentUp{ctype: "text/event-stream", reply: codexReply}
	access := signInCodex(t, f)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","max_output_tokens":50,"temperature":0.2}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil || out["object"] != "response" || !strings.Contains(body, "pong") {
		t.Fatalf("not the final response object: %s", body)
	}
	checkCodexUpstream(t, f, access)
}

func TestCodexStreamingResponsesIsForwarded(t *testing.T) {
	f := &agentUp{ctype: "text/event-stream", reply: codexReply}
	access := signInCodex(t, f)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping","stream":true,"max_output_tokens":50,"temperature":0.2}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "response.completed") {
		t.Errorf("stream not relayed: %s", body)
	}
	checkCodexUpstream(t, f, access)
}

func TestCodexReplyWithoutContentType(t *testing.T) {
	f := &agentUp{ctype: "", reply: codexReply}
	signInCodex(t, f)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("responses client: status %d: %s", code, body)
	}
	code, body = post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"ping"}]}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("chat client: status %d: %s", code, body)
	}
}

// copilotOK is a chat reply with no streaming.
const copilotOK = `{"id":"c1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`

func TestCopilotSessionHeadersAndRefusal(t *testing.T) {
	f := &agentUp{ctype: "application/json", reply: copilotOK}
	exchanges := 0
	var up *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		exchanges++
		if r.Header.Get("Authorization") != "token gho_test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":"sess-1","expires_at":%d,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), up.URL+"/copilot")
	})
	mux.Handle("/copilot/", f)
	up = httptest.NewServer(mux)
	t.Cleanup(up.Close)
	signInCopilot(t, up)

	// a turn that answers a tool result
	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"tool","tool_call_id":"c1","content":"result"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/copilot/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	if f.head.Get("Authorization") != "Bearer sess-1" || f.head.Get("X-Initiator") != "agent" {
		t.Errorf("headers: auth %q initiator %q", f.head.Get("Authorization"), f.head.Get("X-Initiator"))
	}
	var sent map[string]any
	json.Unmarshal(f.body, &sent)
	if sent["model"] != "gpt-4o" {
		t.Errorf("model sent as %v", sent["model"])
	}

	// a turn the user typed reuses the live session
	code, _ = post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"again"}]}`)
	if code != 200 || f.head.Get("X-Initiator") != "user" {
		t.Errorf("second turn: status %d initiator %q", code, f.head.Get("X-Initiator"))
	}
	if exchanges != 1 {
		t.Errorf("session traded %d times, want 1", exchanges)
	}

	// a refused session is reported as a login problem, and the next request trades anew
	f.code, f.reply = http.StatusUnauthorized, `{"error":{"message":"unauthorized"}}`
	code, body = post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if code != 401 || !strings.Contains(body, "请重新登录 Copilot") {
		t.Errorf("refused session: status %d: %s", code, body)
	}
	f.code, f.reply = 0, copilotOK
	code, _ = post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || exchanges != 2 {
		t.Errorf("after refusal: status %d, exchanges %d, want 200 and 2", code, exchanges)
	}
}
