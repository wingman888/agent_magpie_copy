package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// copUpFake plays both GitHub's Copilot token endpoint and the Copilot API host
// the session names. It records what the chat endpoint was sent.
type copUpFake struct {
	chatBody  []byte
	chatHead  http.Header
	chatPath  string
	tokenCode int    // status of the token exchange; 0 means 200
	chatCode  int    // status of the chat call; 0 means 200
	reply     string // SSE body of the chat call
	api       string
}

func (f *copUpFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/copilot_internal/v2/token":
		if f.tokenCode != 0 {
			w.WriteHeader(f.tokenCode)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"token":"sess-up","expires_at":4102444800,"endpoints":{"api":"`+f.api+`"}}`)
	default:
		f.chatBody, _ = io.ReadAll(r.Body)
		f.chatHead, f.chatPath = r.Header.Clone(), r.URL.Path
		if f.chatCode != 0 {
			w.WriteHeader(f.chatCode)
			io.WriteString(w, `{"error":{"message":"no"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, f.reply)
	}
}

// copUpReply is a Chat stream with one text delta and a final usage chunk.
const copUpReply = `data: {"id":"c1","model":"gpt-5.5","choices":[{"index":0,"delta":{"role":"assistant","content":"hi there"}}]}` + "\n\n" +
	`data: {"id":"c1","model":"gpt-5.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n" +
	"data: [DONE]\n\n"

// copUpSetup signs Copilot in, points the provider at f, and returns a gateway.
func copUpSetup(t *testing.T, f *copUpFake) *Server {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg-config")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	path := filepath.Join(xdg, "github-copilot", "apps.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:Iv1":{"user":"octo","oauth_token":"gho_up"}}`
	if err := os.WriteFile(path, []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	f.api = up.URL
	oldURL := provider.CopilotTokenURL
	provider.CopilotTokenURL = up.URL + "/copilot_internal/v2/token"
	t.Cleanup(func() { provider.CopilotTokenURL = oldURL })
	return New()
}

// copUpCall sends one client request through the gateway and returns the reply.
func copUpCall(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestCopUpUserTurnHeaders(t *testing.T) {
	f := &copUpFake{reply: copUpReply}
	s := copUpSetup(t, f)
	rec := copUpCall(t, s, "/v1/chat/completions",
		`{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "hi there") {
		t.Errorf("reply lacks the text: %s", rec.Body.String())
	}
	if f.chatPath != "/chat/completions" {
		t.Errorf("upstream path %q, want /chat/completions", f.chatPath)
	}
	if got := f.chatHead.Get("Authorization"); got != "Bearer sess-up" {
		t.Errorf("Authorization = %q, want the session token", got)
	}
	if got := f.chatHead.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator = %q, want user", got)
	}
	if got := f.chatHead.Get("Copilot-Vision-Request"); got != "" {
		t.Errorf("Copilot-Vision-Request = %q on a text request", got)
	}
	var m map[string]any
	if err := json.Unmarshal(f.chatBody, &m); err != nil {
		t.Fatalf("upstream body: %v", err)
	}
	if m["model"] != "gpt-5.5" {
		t.Errorf("model = %v, want gpt-5.5 without the copilot/ prefix", m["model"])
	}
}

func TestCopUpToolReturnTurnIsAgent(t *testing.T) {
	f := &copUpFake{reply: copUpReply}
	s := copUpSetup(t, f)
	body := `{"model":"copilot/gpt-5.5","messages":[` +
		`{"role":"user","content":"list files"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"a.txt"}]}`
	rec := copUpCall(t, s, "/v1/chat/completions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.chatHead.Get("X-Initiator"); got != "agent" {
		t.Errorf("X-Initiator = %q, want agent for a tool-return turn", got)
	}
}

func TestCopUpImageSetsVisionMarker(t *testing.T) {
	f := &copUpFake{reply: copUpReply}
	s := copUpSetup(t, f)
	body := `{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":[` +
		`{"type":"text","text":"what is this"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,iVBORw0KGgo="}}]}]}`
	rec := copUpCall(t, s, "/v1/chat/completions", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := f.chatHead.Get("Copilot-Vision-Request"); got != "true" {
		t.Errorf("Copilot-Vision-Request = %q, want true", got)
	}
	if got := f.chatHead.Get("X-Initiator"); got != "user" {
		t.Errorf("X-Initiator = %q, want user", got)
	}
}

func TestCopUpSessionRejectedSaysReSignIn(t *testing.T) {
	f := &copUpFake{tokenCode: http.StatusUnauthorized, reply: copUpReply}
	s := copUpSetup(t, f)
	rec := copUpCall(t, s, "/v1/chat/completions",
		`{"model":"copilot/gpt-5.5","messages":[{"role":"user","content":"hello"}]}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请重新登录 Copilot") {
		t.Errorf("body lacks the re-sign-in message: %s", rec.Body.String())
	}
}

func TestCopUpChatRejectedSaysReSignIn(t *testing.T) {
	f := &copUpFake{chatCode: http.StatusForbidden, reply: copUpReply}
	s := copUpSetup(t, f)
	rec := copUpCall(t, s, "/v1/responses",
		`{"model":"copilot/gpt-5.5","input":"hello"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "请重新登录 Copilot") {
		t.Errorf("body lacks the re-sign-in message: %s", rec.Body.String())
	}
}
