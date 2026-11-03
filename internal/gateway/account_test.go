package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// codexFake stands in for the ChatGPT Codex backend and records what it got.
type codexFake struct {
	head  http.Header
	path  string
	body  []byte
	ctype string // "" sends no Content-Type at all
	code  int
	reply string
}

func (c *codexFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.head, c.path = r.Header.Clone(), r.URL.Path
	c.body, _ = io.ReadAll(r.Body)
	if c.ctype != "" {
		w.Header().Set("Content-Type", c.ctype)
	}
	if c.code != 0 {
		w.WriteHeader(c.code)
	}
	io.WriteString(w, c.reply)
}

// claimsJWT builds an unsigned JWT with the given payload.
func claimsJWT(m map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(m)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// codexSetup signs a fake ChatGPT login into a temp HOME and points the Codex
// backend at the fake. It returns the access token the login holds.
func codexSetup(t *testing.T, f *codexFake) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	access := claimsJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      claimsJWT(map[string]any{"email": "me@example.com"}),
			"access_token":  access,
			"refresh_token": "rt-1",
			"account_id":    "acct-1",
		},
	}
	b, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	return access
}

func decodeMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("upstream body is not JSON: %v: %s", err, b)
	}
	return m
}

// assertCodexBody checks the rules every request to the Codex backend obeys.
func assertCodexBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	m := decodeMap(t, b)
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5 without the codex/ prefix", m["model"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v, want true/false", m["stream"], m["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("upstream body carries %s: %s", k, b)
		}
	}
	if _, ok := m["input"].([]any); !ok {
		t.Errorf("input is not a list: %v", m["input"])
	}
	return m
}

func TestCodexNonStreamGetsWholeResponse(t *testing.T) {
	f := &codexFake{reply: sse(
		`data: {"type":"response.output_item.done","item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.5","output":[],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}`)}
	access := codexSetup(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":false,"max_output_tokens":100,"temperature":0.2}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Status string           `json:"status"`
		Output []map[string]any `json:"output"`
		Usage  map[string]any   `json:"usage"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	if out.Status != "completed" || len(out.Output) != 1 || out.Output[0]["type"] != "message" {
		t.Errorf("reply = %s", body)
	}
	if out.Usage["input_tokens"] != float64(10) {
		t.Errorf("usage = %v", out.Usage)
	}
	if f.path != "/backend-api/codex/responses" {
		t.Errorf("upstream path %s", f.path)
	}
	if got := f.head.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q", got)
	}
	if got := f.head.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
	m := assertCodexBody(t, f.body)
	in := m["input"].([]any)
	first := in[0].(map[string]any)
	content := first["content"].([]any)[0].(map[string]any)
	if first["role"] != "user" || content["type"] != "input_text" || content["text"] != "hi" {
		t.Errorf("input item = %v", first)
	}
}

func TestCodexStreamForwardedWithoutContentType(t *testing.T) {
	reply := sse(
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}")
	f := &codexFake{reply: reply}
	codexSetup(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true,"max_output_tokens":9,"temperature":1}`)
	if code != 200 || body != reply {
		t.Fatalf("status %d, body %q", code, body)
	}
	assertCodexBody(t, f.body)
}

func TestCodexChatClientUsesSameUpstreamRules(t *testing.T) {
	f := &codexFake{reply: sse(
		`data: {"type":"response.created","response":{"id":"resp_2","model":"gpt-5.5"}}`,
		`data: {"type":"response.output_text.delta","delta":"yo"}`,
		`data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}}`)}
	codexSetup(t, f)

	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"hi"}],"temperature":0.5,"max_tokens":50}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.Contains(body, "yo") {
		t.Errorf("reply lacks the text: %s", body)
	}
	assertCodexBody(t, f.body)
}

func TestCodexRejectedLoginSaysSignIn(t *testing.T) {
	f := &codexFake{code: 401, ctype: "application/json", reply: `{"error":{"message":"token expired"}}`}
	codexSetup(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":false}`)
	if code != 401 || !strings.Contains(body, "codex login") {
		t.Errorf("status %d, body %s", code, body)
	}
}

func TestCodexRateLimitGivesRealReason(t *testing.T) {
	f := &codexFake{code: 429, ctype: "application/json", reply: `{"error":{"message":"rate limit hit"}}`}
	codexSetup(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":false}`)
	if code != 429 || !strings.Contains(body, "rate limit hit") || strings.Contains(body, "已登出") {
		t.Errorf("status %d, body %s", code, body)
	}
}

func TestCodexServerErrorGivesRealReasonStreaming(t *testing.T) {
	f := &codexFake{code: 500, ctype: "application/json", reply: `{"error":{"message":"upstream exploded"}}`}
	codexSetup(t, f)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":true}`)
	if code != 500 || !strings.Contains(body, "upstream exploded") || strings.Contains(body, "已登出") {
		t.Errorf("status %d, body %s", code, body)
	}
}

func TestCopilotInitiatorAndVisionHeaders(t *testing.T) {
	cases := []struct {
		name string
		body string
		to   provider.Protocol
		want map[string]string
	}{
		{"user turn", `{"messages":[{"role":"user","content":"hi"}]}`, provider.Chat,
			map[string]string{"X-Initiator": "user"}},
		{"tool reply", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null},{"role":"tool","tool_call_id":"c1","content":"ok"}]}`, provider.Chat,
			map[string]string{"X-Initiator": "agent"}},
		{"responses tool output", `{"input":[{"type":"message","role":"user","content":"x"},{"type":"function_call_output","call_id":"c","output":"o"}]}`, provider.Responses,
			map[string]string{"X-Initiator": "agent"}},
		{"image is vision", `{"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`, provider.Chat,
			map[string]string{"X-Initiator": "user", "Copilot-Vision-Request": "true"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := copilotHeaders([]byte(c.body), c.to)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("headers = %v, want %v", got, c.want)
			}
		})
	}
}
