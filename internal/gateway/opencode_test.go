package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDeveloperAsSystem(t *testing.T) {
	in := []byte(`{"model":"m1","stream":true,"messages":[{"role":"developer","content":"be terse"},{"role":"user","content":"hi"}]}`)
	out := developerAsSystem(in)
	var got struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "m1" || !got.Stream || len(got.Messages) != 2 ||
		got.Messages[0].Role != "system" || got.Messages[0].Content != "be terse" ||
		got.Messages[1].Role != "user" || got.Messages[1].Content != "hi" {
		t.Fatalf("rewritten: %s", out)
	}
	// no developer message: the bytes come back untouched
	plain := []byte(`{"model":"m1", "messages":[{"role":"system","content":"x"}]}`)
	if out := developerAsSystem(plain); string(out) != string(plain) {
		t.Fatalf("plain body changed: %s", out)
	}
	// a developer word that is only content leaves the body alone
	quoted := []byte(`{"model":"m1","messages":[{"role":"user","content":"the \"developer\" role"}]}`)
	if out := developerAsSystem(quoted); string(out) != string(quoted) {
		t.Fatalf("content changed: %s", out)
	}
}

// chatUpstream records the body it received and answers with a plain reply.
type chatUpstream struct {
	body    []byte
	headers http.Header
}

func (c *chatUpstream) RoundTrip(r *http.Request) (*http.Response, error) {
	c.body, _ = io.ReadAll(r.Body)
	c.headers = r.Header.Clone()
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(`{"id":"x","object":"chat.completion","choices":[]}`)), Request: r}, nil
}

// newChatProvider saves one Chat provider with the given base and the model m1.
func newChatProvider(t *testing.T, id, base string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: base, Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
}

func serveChat(t *testing.T, srv *Server, model, messages string) {
	t.Helper()
	body := `{"model":"` + model + `","messages":` + messages + `}`
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestChatPassthroughRewritesDeveloper(t *testing.T) {
	f := &fake{t: t}
	up := httptest.NewServer(f)
	defer up.Close()
	newChatProvider(t, "fake", up.URL+"/v1")
	srv := New()
	serveChat(t, srv, "fake/m1", `[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]`)
	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(f.got, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[0].Content != "rules" || got.Messages[1].Role != "user" {
		t.Fatalf("upstream got %s", f.got)
	}
}

func TestOpenCodeGetsSessionHeader(t *testing.T) {
	newChatProvider(t, "opencode-go", "https://opencode.ai/zen/go/v1")
	up := &chatUpstream{}
	srv := New()
	srv.client.Transport = up

	msgs := `[{"role":"user","content":"plan the refactor"},{"role":"assistant","content":"ok"},{"role":"user","content":"go on"}]`
	serveChat(t, srv, "opencode-go/m1", msgs)
	first := up.headers.Get("x-opencode-session")
	if !strings.HasPrefix(first, "magpie-") {
		t.Fatalf("derived session header: %q", first)
	}
	// the same conversation a turn later keeps its id
	serveChat(t, srv, "opencode-go/m1", `[{"role":"user","content":"plan the refactor"},{"role":"assistant","content":"ok"},{"role":"user","content":"go on"},{"role":"assistant","content":"done"},{"role":"user","content":"thanks"}]`)
	if got := up.headers.Get("x-opencode-session"); got != first {
		t.Fatalf("later turn: %q, want %q", got, first)
	}
	// another conversation gets its own id
	serveChat(t, srv, "opencode-go/m1", `[{"role":"user","content":"something else"}]`)
	if got := up.headers.Get("x-opencode-session"); got == first {
		t.Fatalf("different conversation shares %q", got)
	}
	// the agent's own session id is used as-is
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"opencode-go/m1","messages":`+msgs+`}`))
	req.Header.Set("X-Session-Id", "from-agent")
	srv.Handler().ServeHTTP(rec, req)
	if got := up.headers.Get("x-opencode-session"); got != "from-agent" {
		t.Fatalf("agent header: %q", got)
	}
}

func TestOtherProvidersNeverGetSessionHeader(t *testing.T) {
	newChatProvider(t, "deepseek", "https://api.deepseek.com/v1")
	up := &chatUpstream{}
	srv := New()
	srv.client.Transport = up
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"deepseek/m1","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("X-Session-Id", "from-agent")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if v, ok := up.headers["X-Opencode-Session"]; ok {
		t.Fatalf("non-OpenCode upstream got %v", v)
	}
}
