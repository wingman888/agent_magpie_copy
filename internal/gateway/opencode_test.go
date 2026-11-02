package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDeveloperBecomesSystem(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"x"}`}
	setup(t, provider.Chat, f)
	code, _ := post(t, "/v1/chat/completions", `{"model":"fake/m1","messages":[{"role":"developer","content":"be brief","name":"d"},{"role":"user","content":"hi"}],"foo":1}`)
	if code != 200 {
		t.Fatal(code)
	}
	got := string(f.got)
	if !strings.Contains(got, `"role":"system"`) || strings.Contains(got, "developer") || !strings.Contains(got, `"be brief"`) || !strings.Contains(got, `"name":"d"`) || !strings.Contains(got, `"foo":1`) {
		t.Fatalf("body: %s", got)
	}
	plain := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	if out := developerToSystem([]byte(plain)); string(out) != plain {
		t.Fatalf("changed: %s", out)
	}
	if f.head.Get("x-opencode-session") != "" {
		t.Fatal("non-opencode got session header")
	}
}

type rt func(*http.Request) (*http.Response, error)

func (f rt) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func opencodeCall(t *testing.T, path, body string, hdr map[string]string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "oc", Name: "OC", Key: "k", Models: []string{"m1"},
		Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	var seen string
	s := New()
	s.client = &http.Client{Transport: rt(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Get("x-opencode-session")
		if r.URL.Host != "opencode.ai" {
			t.Errorf("host %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{"id":"x"}`)), Request: r}, nil
	})}
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	return seen
}

func TestOpenCodeSession(t *testing.T) {
	chat := func(first string) string {
		return `{"model":"oc/m1","messages":[{"role":"system","content":"s"},{"role":"user","content":"` + first + `"},{"role":"assistant","content":"a"},{"role":"user","content":"more"}]}`
	}
	a := opencodeCall(t, "/v1/chat/completions", chat("one"), nil)
	b := opencodeCall(t, "/v1/chat/completions", chat("one"), nil)
	c := opencodeCall(t, "/v1/chat/completions", chat("two"), nil)
	if !strings.HasPrefix(a, "magpie-") || a != b || a == c {
		t.Fatalf("%q %q %q", a, b, c)
	}
	r1 := opencodeCall(t, "/v1/responses", `{"model":"oc/m1","input":"hello"}`, nil)
	r2 := opencodeCall(t, "/v1/responses", `{"model":"oc/m1","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]},{"role":"user","content":"x"}]}`, nil)
	r3 := opencodeCall(t, "/v1/responses", `{"model":"oc/m1","input":"other"}`, nil)
	if !strings.HasPrefix(r1, "magpie-") || r1 != r2 || r1 == r3 {
		t.Fatalf("%q %q %q", r1, r2, r3)
	}
	if got := opencodeCall(t, "/v1/chat/completions", chat("one"), map[string]string{"x-session-id": "S2", "session_id": "S3"}); got != "S2" {
		t.Fatalf("agent header: %q", got)
	}
	if got := opencodeCall(t, "/v1/chat/completions", chat("one"), map[string]string{"x-claude-code-session-id": "C", "x-opencode-session": "O"}); got != "O" {
		t.Fatalf("priority: %q", got)
	}
}
