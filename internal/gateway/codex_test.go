package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// codexSetup gives the test a ChatGPT login for Codex under a temporary home
// and points the Codex backend at a fake server running handler.
func codexSetup(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	auth := `{"auth_mode":"chatgpt","tokens":{"id_token":"x.y.z","access_token":"acc-tok","refresh_token":"r","account_id":"acct-1"}}`
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(handler)
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
}

const codexReply = "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.5\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n"

// codexSeen is what the fake backend received.
type codexSeen struct {
	path string
	head http.Header
	body []byte
}

func assertCodexBody(t *testing.T, seen codexSeen) {
	t.Helper()
	if seen.path != "/backend-api/codex/responses" {
		t.Errorf("path %s", seen.path)
	}
	if seen.head.Get("Authorization") != "Bearer acc-tok" || seen.head.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("auth headers: %v", seen.head)
	}
	for _, banned := range []string{`"max_output_tokens"`, `"temperature"`} {
		if bytes.Contains(seen.body, []byte(banned)) {
			t.Errorf("backend body has %s: %s", banned, seen.body)
		}
	}
	if !bytes.Contains(seen.body, []byte(`"stream":true`)) || !bytes.Contains(seen.body, []byte(`"store":false`)) {
		t.Errorf("backend body not streamed/unstored: %s", seen.body)
	}
	if !bytes.Contains(seen.body, []byte(`"model":"gpt-5.5"`)) || !bytes.Contains(seen.body, []byte(`"input":[`)) {
		t.Errorf("backend body model/input: %s", seen.body)
	}
}

func TestCodexNonStreamIsGathered(t *testing.T) {
	var seen codexSeen
	codexSetup(t, func(w http.ResponseWriter, r *http.Request) {
		seen.path, seen.head = r.URL.Path, r.Header
		seen.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, codexReply)
	})
	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":100,"temperature":0.2,"stream":false}`)
	if code != 200 || !strings.Contains(body, `"id":"resp_1"`) || strings.Contains(body, "event:") {
		t.Fatalf("%d %s", code, body)
	}
	assertCodexBody(t, seen)
}

func TestCodexStreamWithoutContentType(t *testing.T) {
	var seen codexSeen
	codexSetup(t, func(w http.ResponseWriter, r *http.Request) {
		seen.path, seen.head = r.URL.Path, r.Header
		seen.body, _ = io.ReadAll(r.Body)
		// an empty value keeps net/http from sniffing a Content-Type
		w.Header()["Content-Type"] = []string{}
		io.WriteString(w, codexReply)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi","max_output_tokens":100,"temperature":0.2,"stream":true}`))
	New().Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if !strings.Contains(rec.Body.String(), "response.output_text.delta") || !strings.Contains(rec.Body.String(), "response.completed") {
		t.Errorf("stream not relayed: %s", rec.Body.String())
	}
	assertCodexBody(t, seen)
}

func TestChatClientReachesCodexThroughTranslation(t *testing.T) {
	var seen codexSeen
	codexSetup(t, func(w http.ResponseWriter, r *http.Request) {
		seen.path, seen.head = r.URL.Path, r.Header
		seen.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, codexReply)
	})
	code, body := post(t, "/v1/chat/completions", `{"model":"codex/gpt-5.5","max_tokens":20,"temperature":0.3,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hi") {
		t.Fatalf("%d %s", code, body)
	}
	assertCodexBody(t, seen)
}
