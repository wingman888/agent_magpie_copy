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

func codexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	jwt := func(c map[string]any) string { return enc(map[string]any{"alg": "none"}) + "." + enc(c) + ".s" }
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":     jwt(map[string]any{"email": "me@x.com"}),
		"access_token": jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}), "refresh_token": "r", "account_id": "acct-9"}}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	b, _ := json.Marshal(auth)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600)
}

func codexBackend(t *testing.T, reply string, ctype bool) (*[]byte, *http.Header) {
	var got []byte
	var head http.Header
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		got, _ = io.ReadAll(r.Body)
		head = r.Header
		if ctype {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header()["Content-Type"] = nil // no Content-Type at all
		}
		io.WriteString(w, reply)
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL
	t.Cleanup(func() { provider.CodexBase = old })
	return &got, &head
}

var codexReply = sse(
	`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","object":"response","status":"in_progress"}}`,
	`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","output_index":0,"delta":"Hel"}`,
	`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","output_index":0,"delta":"lo"}`,
	`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":3,"output_tokens":2}}}`)

func TestCodexNonStreamFolded(t *testing.T) {
	codexHome(t)
	got, head := codexBackend(t, codexReply, false)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":50,"temperature":0.2}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var m map[string]any
	json.Unmarshal([]byte(body), &m)
	if m["object"] != "response" || !strings.Contains(body, "Hello") {
		t.Fatalf("%s", body)
	}
	var sent map[string]any
	json.Unmarshal(*got, &sent)
	if sent["model"] != "gpt-5.5" || sent["stream"] != true || sent["store"] != false {
		t.Fatalf("%s", *got)
	}
	if _, ok := sent["max_output_tokens"]; ok {
		t.Fatalf("%s", *got)
	}
	if _, ok := sent["input"].([]any); !ok {
		t.Fatalf("%s", *got)
	}
	if head.Get("chatgpt-account-id") != "acct-9" || !strings.HasPrefix(head.Get("Authorization"), "Bearer ") {
		t.Fatal(*head)
	}
}

func TestCodexNonStreamWithoutCompletedEvent(t *testing.T) {
	codexHome(t)
	codexBackend(t, sse(
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"Hi "}`,
		`data: {"type":"response.output_text.delta","output_index":0,"delta":"you"}`), true)
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 200 || !strings.Contains(body, "Hi you") || !strings.Contains(body, `"object":"response"`) {
		t.Fatalf("%d %s", code, body)
	}
}

func TestCodexStreamPassthroughNoContentType(t *testing.T) {
	codexHome(t)
	got, _ := codexBackend(t, codexReply, false)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","stream":true,"input":"hi","top_p":1}`)))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Body.String(), "response.completed") {
		t.Fatal(rec.Body.String())
	}
	if strings.Contains(string(*got), "top_p") {
		t.Fatal(string(*got))
	}
}

func TestSignedOutIs401(t *testing.T) {
	codexHome(t)
	os.Remove(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"))
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi"}`)
	if code != 404 {
		t.Fatalf("%d %s", code, body)
	}
}

func TestSniffContentType(t *testing.T) {
	for body, want := range map[string]string{"\n data: {}\n\n": "text/event-stream", "event: x\n": "text/event-stream", `{"a":1}`: "", "da": ""} {
		res := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
		sniffContentType(res)
		if res.Header.Get("Content-Type") != want {
			t.Errorf("%q → %q", body, res.Header.Get("Content-Type"))
		}
		if b, _ := io.ReadAll(res.Body); string(b) != body {
			t.Errorf("body changed: %q", b)
		}
	}
}
