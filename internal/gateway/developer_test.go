package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDeveloperToSystem(t *testing.T) {
	in := []byte(`{"model":"m1","temperature":0.5,"messages":[{"role":"developer","content":"be terse"},{"role":"user","content":"hi"}]}`)
	out := developerToSystem(in)
	var got struct {
		Model       string           `json:"model"`
		Temperature float64          `json:"temperature"`
		Messages    []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.Model != "m1" || got.Temperature != 0.5 || len(got.Messages) != 2 {
		t.Fatalf("other fields changed: %s", out)
	}
	if got.Messages[0]["role"] != "system" || got.Messages[0]["content"] != "be terse" || got.Messages[1]["role"] != "user" || got.Messages[1]["content"] != "hi" {
		t.Errorf("messages: %s", out)
	}

	plain := []byte(`{"model":"m1","messages":[{"role":"system","content":"x"},{"role":"user","content":"developer"}]}`)
	if got := developerToSystem(plain); !bytes.Equal(got, plain) {
		t.Errorf("body without developer role changed: %s", got)
	}
	// the word alone, inside content, must not trigger a rewrite either
	quoted := []byte(`{"messages":[{"role":"user","content":"\"developer\""}]}`)
	if got := developerToSystem(quoted); !bytes.Equal(got, quoted) {
		t.Errorf("quoted word changed body: %s", got)
	}
}

func TestDeveloperRoleReachesChatUpstreamAsSystem(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c1","object":"chat.completion","choices":[]}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if !bytes.Contains(f.got, []byte(`"role":"system"`)) || bytes.Contains(f.got, []byte(`"developer"`)) || !bytes.Contains(f.got, []byte(`"content":"rules"`)) {
		t.Errorf("upstream body: %s", f.got)
	}
}

// redirect sends every request to one local address, keeping the URL the
// gateway built, so tests can exercise hosts such as opencode.ai.
type redirect struct{ addr string }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	c := req.Clone(req.Context())
	c.URL.Scheme = "http"
	c.URL.Host = r.addr
	c.Host = r.addr
	return http.DefaultTransport.RoundTrip(c)
}

func TestOpenCodeGatewaySessionHeader(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"r1","object":"response","output":[]}`}
	up := httptest.NewServer(f)
	defer up.Close()
	addr := strings.TrimPrefix(up.URL, "http://")
	if err := provider.Save(provider.Provider{ID: "oc", Name: "OC", Key: "k", Models: []string{"m1"},
		Responses: "https://opencode.ai/zen/go/v1", Chat: "https://opencode.ai/zen/go/v1"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.client = &http.Client{Transport: redirect{addr: addr}}
	send := func(path, body string, hdr map[string]string) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}

	// no agent session: a stable magpie- id for the conversation
	send("/v1/responses", `{"model":"oc/m1","input":"first question"}`, nil)
	first := f.head.Get("x-opencode-session")
	if !strings.HasPrefix(first, "magpie-") {
		t.Fatalf("no magpie session sent: %q", first)
	}
	send("/v1/responses", `{"model":"oc/m1","input":[{"type":"message","role":"user","content":"first question"}]}`, nil)
	if got := f.head.Get("x-opencode-session"); got != first {
		t.Errorf("later turn session %q, want %q", got, first)
	}
	send("/v1/responses", `{"model":"oc/m1","input":"another chat"}`, nil)
	if got := f.head.Get("x-opencode-session"); got == first {
		t.Errorf("different conversation shares session %q", got)
	}

	// the agent's own session id is used as-is
	send("/v1/responses", `{"model":"oc/m1","input":"first question"}`, map[string]string{"session_id": "agent-42"})
	if got := f.head.Get("x-opencode-session"); got != "agent-42" {
		t.Errorf("agent session %q, want agent-42", got)
	}

	// Chat Completions too
	send("/v1/chat/completions", `{"model":"oc/m1","messages":[{"role":"user","content":"first question"}]}`, nil)
	if got := f.head.Get("x-opencode-session"); got != first {
		t.Errorf("chat session %q, want %q", got, first)
	}
}

func TestNonOpenCodeUpstreamGetsNoSessionHeader(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"r1","object":"response","output":[]}`}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","input":"hi"}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if got := f.head.Get("x-opencode-session"); got != "" {
		t.Errorf("non-OpenCode upstream got session header %q", got)
	}
}
