package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// cpTFake is a fake Copilot: one server that answers the session endpoint
// and, as the session's API host, the Chat upstream. It records what the
// upstream got. sessionStatus or upStatus, when set, replace the normal reply.
type cpTFake struct {
	url           string
	sessionStatus int
	upStatus      int
	reply         string
	head          http.Header
	path          string
	body          []byte
}

func (f *cpTFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/copilot_internal/v2/token") {
		if f.sessionStatus != 0 {
			w.WriteHeader(f.sessionStatus)
			return
		}
		if r.Header.Get("Authorization") != "token gho_cpt" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"token":"sess-tok","expires_at":4102444800,"endpoints":{"api":%q}}`, f.url)
		return
	}
	f.head, f.path = r.Header.Clone(), r.URL.Path
	f.body, _ = io.ReadAll(r.Body)
	if f.upStatus != 0 {
		w.WriteHeader(f.upStatus)
		io.WriteString(w, `{"error":{"message":"token expired"}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, f.reply)
}

// cpTReply is a Chat stream that says "hi".
const cpTReply = `data: {"id":"c1","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}

data: {"id":"c1","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}

data: [DONE]

`

// cpTSetup signs Copilot in (a fresh XDG config dir with apps.json) and points
// the session endpoint at the fake.
func cpTSetup(t *testing.T, f *cpTFake) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := filepath.Join(xdg, "github-copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:Iv1.test":{"user":"octo","oauth_token":"gho_cpt"}}`
	if err := os.WriteFile(filepath.Join(dir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	old := provider.CopilotTokenURL
	provider.CopilotTokenURL = srv.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = old })
}

// cpTPost sends a request to the gateway and returns the recorder.
func cpTPost(t *testing.T, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec
}

func TestCpTUserTurnHeadersAndBody(t *testing.T) {
	f := &cpTFake{reply: cpTReply}
	cpTSetup(t, f)
	rec := cpTPost(t, "/v1/chat/completions", `{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if f.path != "/chat/completions" {
		t.Errorf("upstream path %q", f.path)
	}
	if got := f.head.Get("Authorization"); got != "Bearer sess-tok" {
		t.Errorf("Authorization %q", got)
	}
	if got := f.head.Get("Copilot-Integration-Id"); got != "vscode-chat" {
		t.Errorf("Copilot-Integration-Id %q", got)
	}
	if got := f.head.Get("Editor-Version"); got == "" {
		t.Error("Editor-Version missing")
	}
	if got := f.head.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator %q, want user", got)
	}
	if got := f.head.Get("Copilot-Vision-Request"); got != "" {
		t.Errorf("Copilot-Vision-Request %q on a text turn", got)
	}
	var got map[string]any
	if err := json.Unmarshal(f.body, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "gpt-5.5" {
		t.Errorf("upstream model %v, want gpt-5.5 without copilot/", got["model"])
	}
	if !strings.Contains(rec.Body.String(), "hi") {
		t.Errorf("client reply lacks the text: %s", rec.Body.String())
	}
}

func TestCpTToolTurnIsAgentAndImageIsVision(t *testing.T) {
	f := &cpTFake{reply: cpTReply}
	cpTSetup(t, f)
	body := `{"model":"copilot/gpt-5.5","messages":[
		{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"file body"}]}`
	rec := cpTPost(t, "/v1/chat/completions", body)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.head.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator %q, want agent for a tool-result turn", got)
	}
	if got := f.head.Get("Copilot-Vision-Request"); got != "true" {
		t.Errorf("Copilot-Vision-Request %q, want true for an image", got)
	}
}

func TestCpTResponsesClientGoesThroughChat(t *testing.T) {
	f := &cpTFake{reply: cpTReply}
	cpTSetup(t, f)
	rec := cpTPost(t, "/v1/responses", `{"model":"copilot/gpt-5.5","input":"hi"}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if f.path != "/chat/completions" {
		t.Errorf("upstream path %q, want /chat/completions", f.path)
	}
}

func TestCpTSessionFailureAsksReLogin(t *testing.T) {
	f := &cpTFake{sessionStatus: http.StatusUnauthorized, reply: cpTReply}
	cpTSetup(t, f)
	rec := cpTPost(t, "/v1/chat/completions", `{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code == 200 {
		t.Fatalf("expected a failure, got 200: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请重新登录 Copilot") {
		t.Errorf("body lacks the re-login hint: %s", rec.Body.String())
	}
}

func TestCpTUpstreamAuthFailureAsksReLogin(t *testing.T) {
	f := &cpTFake{upStatus: http.StatusUnauthorized, reply: cpTReply}
	cpTSetup(t, f)
	rec := cpTPost(t, "/v1/chat/completions", `{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请重新登录 Copilot") {
		t.Errorf("body lacks the re-login hint: %s", rec.Body.String())
	}
}
