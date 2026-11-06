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
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// accountHome isolates HOME and XDG dirs and writes a ChatGPT Codex login.
func accountHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeCodexAuth(t *testing.T, home string, accessExp int64) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "dev@example.com"}),
			"access_token":  jwt(map[string]any{"exp": accessExp}),
			"refresh_token": "rt-1",
			"account_id":    "acct-1",
		},
	})
	p := filepath.Join(home, ".codex", "auth.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCopilotAuth(t *testing.T) {
	t.Helper()
	p := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"github.com:Iv1.x":{"user":"octocat","oauth_token":"gho_x"}}`), 0o600)
}

// fakeCodex stands in for the ChatGPT Codex backend.
type fakeCodex struct {
	path   string
	head   http.Header
	got    map[string]any
	ctype  string // empty: send no Content-Type
	reply  string
	status int
}

func (f *fakeCodex) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.path, f.head = r.URL.Path, r.Header.Clone()
	f.got = map[string]any{}
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &f.got)
	if f.ctype != "" {
		w.Header().Set("Content-Type", f.ctype)
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
	}
	io.WriteString(w, f.reply)
}

const codexReply = `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hi "}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"there"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}

`

func withCodex(t *testing.T, f *fakeCodex) {
	t.Helper()
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL
	t.Cleanup(func() { provider.CodexBase = old })
}

func TestCodexNonStreamingResponsesIsAggregated(t *testing.T) {
	home := accountHome(t)
	writeCodexAuth(t, home, time.Now().Add(time.Hour).Unix())
	f := &fakeCodex{ctype: "text/event-stream", reply: codexReply}
	withCodex(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hello","max_output_tokens":100,"temperature":0.5,"top_p":0.9}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	if !strings.Contains(body, "Hi there") {
		t.Errorf("reply lacks the aggregated text: %s", body)
	}

	if f.path != "/responses" {
		t.Errorf("upstream path %q, want /responses", f.path)
	}
	if got := f.head.Get("Authorization"); got != "Bearer "+tokenOf(t, home) {
		t.Errorf("Authorization = %q", got)
	}
	if got := f.head.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	if f.got["model"] != "gpt-5.5" || f.got["stream"] != true || f.got["store"] != false {
		t.Errorf("upstream body model/stream/store: %v", f.got)
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := f.got[k]; ok {
			t.Errorf("upstream body still has %s", k)
		}
	}
	items, ok := f.got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("string input not turned into a list: %v", f.got["input"])
	}
}

func tokenOf(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	json.Unmarshal(b, &f)
	return f.Tokens.AccessToken
}

func TestCodexStreamingIsRelayedWithoutContentType(t *testing.T) {
	home := accountHome(t)
	writeCodexAuth(t, home, time.Now().Add(time.Hour).Unix())
	f := &fakeCodex{ctype: "", reply: codexReply}
	withCodex(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":[{"role":"user","content":"hi"}],"stream":true,"max_output_tokens":50,"temperature":1}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "response.completed") {
		t.Errorf("stream not relayed: %s", body)
	}
	if f.got["stream"] != true || f.got["store"] != false {
		t.Errorf("upstream body: %v", f.got)
	}
	if _, ok := f.got["max_output_tokens"]; ok {
		t.Errorf("max_output_tokens sent upstream")
	}
	if _, ok := f.got["temperature"]; ok {
		t.Errorf("temperature sent upstream")
	}
}

func TestCodexSignedOutReportsLoginCommand(t *testing.T) {
	home := accountHome(t)
	// expiring token and a refresh endpoint that refuses it
	writeCodexAuth(t, home, time.Now().Add(time.Minute).Unix())
	refresh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	t.Cleanup(refresh.Close)
	old := provider.CodexRefreshURL
	provider.CodexRefreshURL = refresh.URL
	t.Cleanup(func() { provider.CodexRefreshURL = old })
	f := &fakeCodex{ctype: "text/event-stream", reply: codexReply}
	withCodex(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code == 200 {
		t.Fatalf("expected an error, got 200: %s", body)
	}
	if !strings.Contains(body, "codex login") {
		t.Errorf("error does not say to run codex login: %s", body)
	}
	if f.path != "" {
		t.Errorf("request reached Codex despite a failed refresh")
	}
}

// fakeCopilot serves the GitHub session exchange and the Copilot chat API.
type fakeCopilot struct {
	chatHead  http.Header
	chatPath  string
	chatBody  map[string]any
	sessionOK bool
}

func (f *fakeCopilot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/copilot_internal/v2/token":
		if !f.sessionOK || r.Header.Get("Authorization") != "token gho_x" {
			http.Error(w, "bad", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"token":     "cop-session",
			"endpoints": map[string]string{"api": "http://" + r.Host},
		})
	case "/chat/completions":
		f.chatPath, f.chatHead = r.URL.Path, r.Header.Clone()
		f.chatBody = map[string]any{}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &f.chatBody)
		if r.Header.Get("Authorization") != "Bearer cop-session" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"id\":\"c1\",\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	default:
		http.NotFound(w, r)
	}
}

func withCopilot(t *testing.T, f *fakeCopilot) {
	t.Helper()
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = up.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = old })
}

func TestCopilotSessionAndInitiatorHeaders(t *testing.T) {
	accountHome(t)
	writeCopilotAuth(t)
	f := &fakeCopilot{sessionOK: true}
	withCopilot(t, f)

	// the last message is a tool's return: an agent turn
	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[
		{"role":"user","content":"read it"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"t1","content":"data"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.chatPath != "/chat/completions" {
		t.Errorf("upstream path %q", f.chatPath)
	}
	if got := f.chatHead.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator = %q, want agent", got)
	}
	if f.chatHead.Get("Copilot-Vision-Request") != "" {
		t.Errorf("vision header set on a text request")
	}
	if f.chatBody["model"] != "gpt-4o" || f.chatBody["stream"] != true {
		t.Errorf("upstream body: %v", f.chatBody)
	}
}

func TestCopilotUserTurnAndVisionHeaders(t *testing.T) {
	accountHome(t)
	writeCopilotAuth(t)
	f := &fakeCopilot{sessionOK: true}
	withCopilot(t, f)

	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[
		{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if got := f.chatHead.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator = %q, want user", got)
	}
	if got := f.chatHead.Get("Copilot-Vision-Request"); got != "true" {
		t.Errorf("Copilot-Vision-Request = %q, want true", got)
	}
}

func TestCopilotRejectedLoginAsksForSignIn(t *testing.T) {
	accountHome(t)
	writeCopilotAuth(t)
	f := &fakeCopilot{sessionOK: false}
	withCopilot(t, f)

	code, body := post(t, "/v1/chat/completions", `{"model":"copilot/gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if code == 200 {
		t.Fatalf("expected an error: %s", body)
	}
	if !strings.Contains(body, "请重新登录 Copilot") {
		t.Errorf("error does not ask to sign in again: %s", body)
	}
}

func TestCodexBodyRules(t *testing.T) {
	out, err := codexBody([]byte(`{"model":"x","input":"hi","max_output_tokens":9,"temperature":1,"top_p":1,"stream":false,"store":true,"instructions":"be brief"}`), "codex/gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	if m["model"] != "gpt-5.5" || m["stream"] != true || m["store"] != false || m["instructions"] != "be brief" {
		t.Errorf("codexBody = %s", out)
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s kept: %s", k, out)
		}
	}
}
