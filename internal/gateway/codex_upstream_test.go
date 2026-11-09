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

// codexUpFake plays the ChatGPT Codex backend: it records what it was sent and
// answers with a fixed body.
type codexUpFake struct {
	body  []byte
	head  http.Header
	path  string
	reply string
	noCT  bool // send no Content-Type, as the real backend sometimes does
}

func (f *codexUpFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.body, _ = io.ReadAll(r.Body)
	f.head, f.path = r.Header.Clone(), r.URL.Path
	if f.noCT {
		w.Header()["Content-Type"] = nil // present but empty: net/http must not sniff one
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, f.reply)
}

// codexUpJWT builds an unsigned JWT with the given payload.
func codexUpJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// codexUpSetup signs Codex in with a fresh access token, points the backend at
// f, and returns the access token.
func codexUpSetup(t *testing.T, f *codexUpFake) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	access := codexUpJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	doc := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      codexUpJWT(map[string]any{"email": "dev@example.com"}),
			"access_token":  access,
			"refresh_token": "rt-unused",
			"account_id":    "acct-1",
		},
	}
	b, _ := json.Marshal(doc)
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL
	t.Cleanup(func() { provider.CodexBase = old })
	return access
}

// codexUpReply is a Responses stream: one text item, then completion with usage.
const codexUpReply = "event: response.created\n" +
	`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}` + "\n\n" +
	"event: response.output_text.delta\n" +
	`data: {"type":"response.output_text.delta","delta":"hi there"}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.5","status":"completed","usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}` + "\n\n"

// codexUpCheckBody checks the fields every Codex request must carry or lack.
func codexUpCheckBody(t *testing.T, got []byte) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("upstream body is not JSON: %v: %s", err, got)
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5 without the codex/ prefix", m["model"])
	}
	if m["stream"] != true || m["store"] != false {
		t.Errorf("stream/store = %v/%v, want true/false", m["stream"], m["store"])
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s was sent upstream: %s", k, got)
		}
	}
	if _, ok := m["input"].([]any); !ok {
		t.Errorf("input is not a list: %v", m["input"])
	}
}

func codexUpCheckHeaders(t *testing.T, f *codexUpFake, access string) {
	t.Helper()
	if f.path != "/responses" {
		t.Errorf("upstream path %q, want /responses", f.path)
	}
	if got := f.head.Get("Authorization"); got != "Bearer "+access {
		t.Errorf("Authorization = %q", got)
	}
	if got := f.head.Get("chatgpt-account-id"); got != "acct-1" {
		t.Errorf("chatgpt-account-id = %q", got)
	}
}

func TestCodexUpNonStreamResponsesFoldsStream(t *testing.T) {
	f := &codexUpFake{reply: codexUpReply, noCT: true}
	access := codexUpSetup(t, f)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hello","temperature":0.5,"max_output_tokens":100}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("reply is not a whole JSON object: %v: %s", err, body)
	}
	if out["object"] != "response" || out["status"] != "completed" {
		t.Errorf("reply = %s", body)
	}
	if !strings.Contains(body, `"text":"hi there"`) {
		t.Errorf("text missing from reply: %s", body)
	}
	codexUpCheckHeaders(t, f, access)
	codexUpCheckBody(t, f.body)
}

func TestCodexUpStreamResponsesIsRelayed(t *testing.T) {
	f := &codexUpFake{reply: codexUpReply}
	access := codexUpSetup(t, f)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","stream":true,"input":[{"role":"user","content":"hi"}],"temperature":0.2,"top_p":0.9,"max_output_tokens":50}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, "event: response.output_text.delta") || !strings.Contains(body, "hi there") {
		t.Errorf("stream not relayed: %s", body)
	}
	codexUpCheckHeaders(t, f, access)
	codexUpCheckBody(t, f.body)
}

func TestCodexUpChatClientGetsWholeReply(t *testing.T) {
	f := &codexUpFake{reply: codexUpReply, noCT: true}
	access := codexUpSetup(t, f)
	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","temperature":0.3,"max_tokens":20,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, `"choices"`) || !strings.Contains(body, "hi there") {
		t.Errorf("chat reply = %s", body)
	}
	codexUpCheckHeaders(t, f, access)
	codexUpCheckBody(t, f.body)
}

func TestCodexUpStreamWithoutContentTypeIsStream(t *testing.T) {
	f := &codexUpFake{reply: codexUpReply, noCT: true}
	codexUpSetup(t, f)
	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !strings.Contains(body, "hi there") {
		t.Errorf("text lost from a stream with no Content-Type: %s", body)
	}
}
