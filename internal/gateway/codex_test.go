package gateway

import (
	"bytes"
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

// s2Upstream stands in for the ChatGPT Codex backend. It records the request
// and replies with a fixed event stream; noType drops the Content-Type header.
type s2Upstream struct {
	head   http.Header
	path   string
	body   []byte
	reply  string
	noType bool
}

func (u *s2Upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.head, u.path = r.Header.Clone(), r.URL.Path
	u.body, _ = io.ReadAll(r.Body)
	if u.noType {
		w.Header()["Content-Type"] = nil
	} else {
		w.Header().Set("Content-Type", "text/event-stream")
	}
	io.WriteString(w, u.reply)
}

// s2Login writes a ChatGPT login for Codex and points the backend at up.
func s2Login(t *testing.T, up *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	jwt := func(c map[string]any) string {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		b, _ := json.Marshal(c)
		return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	}
	doc := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "dev@example.com"}),
			"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "rt",
			"account_id":    "acct_1",
		},
	}
	b, _ := json.Marshal(doc)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

const s2Reply = "event: response.output_item.done\n" +
	`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}}` + "\n\n" +
	"event: response.completed\n" +
	`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}` + "\n\n"

func TestS2CodexNonStreamingResponsesIsAggregated(t *testing.T) {
	u := &s2Upstream{reply: s2Reply}
	up := httptest.NewServer(u)
	defer up.Close()
	s2Login(t, up)

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","stream":false,"temperature":0.5,"max_output_tokens":9}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("reply is not one JSON object: %v: %s", err, body)
	}
	if out["status"] != "completed" || !strings.Contains(body, `"text":"hello"`) {
		t.Errorf("aggregated reply = %s", body)
	}
	if u.path != "/codex/responses" {
		t.Errorf("upstream path %s", u.path)
	}
	if auth := u.head.Get("Authorization"); !strings.HasPrefix(auth, "Bearer ") || len(auth) < len("Bearer ")+10 {
		t.Errorf("authorization header = %q", auth)
	}
	if u.head.Get("chatgpt-account-id") != "acct_1" {
		t.Errorf("account header = %q", u.head.Get("chatgpt-account-id"))
	}
	var sent map[string]any
	json.Unmarshal(u.body, &sent)
	if sent["model"] != "gpt-5.5" || sent["stream"] != true || sent["store"] != false {
		t.Errorf("upstream body: %s", u.body)
	}
	for _, k := range []string{"max_output_tokens", "temperature"} {
		if _, ok := sent[k]; ok {
			t.Errorf("%s reached the backend: %s", k, u.body)
		}
	}
	if list, ok := sent["input"].([]any); !ok || len(list) == 0 {
		t.Errorf("input is not a list: %s", u.body)
	}
}

func TestS2CodexStreamWithoutContentTypeIsForwarded(t *testing.T) {
	u := &s2Upstream{reply: s2Reply, noType: true}
	up := httptest.NewServer(u)
	defer up.Close()
	s2Login(t, up)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi","stream":true,"temperature":0.5}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type %q, want an event stream", ct)
	}
	if !strings.Contains(rec.Body.String(), "event: response.completed") {
		t.Errorf("stream not relayed: %s", rec.Body.String())
	}
	if bytes.Contains(u.body, []byte("temperature")) {
		t.Errorf("temperature reached the backend: %s", u.body)
	}
	var sent map[string]any
	json.Unmarshal(u.body, &sent)
	if sent["stream"] != true {
		t.Errorf("stream flag not set upstream: %s", u.body)
	}
}

func TestS2ChatClientOnCodexWithoutContentType(t *testing.T) {
	u := &s2Upstream{noType: true, reply: "event: response.output_text.delta\n" +
		`data: {"type":"response.output_text.delta","delta":"pong"}` + "\n\n" +
		"event: response.completed\n" +
		`data: {"type":"response.completed","response":{"id":"resp_2","status":"completed","usage":{"input_tokens":3,"output_tokens":1}}}` + "\n\n"}
	up := httptest.NewServer(u)
	defer up.Close()
	s2Login(t, up)

	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","messages":[{"role":"user","content":"ping"}]}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	if u.head.Get("chatgpt-account-id") != "acct_1" {
		t.Errorf("account header missing: %v", u.head)
	}
}
