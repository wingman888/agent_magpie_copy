package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// s2ghRewrite sends every request to a local fake, keeping the request's own
// URL (and so its host) as the gateway built it.
type s2ghRewrite struct{ to *url.URL }

func (t s2ghRewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme, r.URL.Host, r.Host = t.to.Scheme, t.to.Host, t.to.Host
	return http.DefaultTransport.RoundTrip(r)
}

// s2ghServer makes a gateway with one provider whose Chat base is base, and
// an upstream fake that records what arrived.
func s2ghServer(t *testing.T, f *fake, base string) *Server {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	up := httptest.NewServer(f)
	t.Cleanup(up.Close)
	if base == "" {
		base = up.URL + "/v1"
	}
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}, Chat: base}); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	s.client = &http.Client{Transport: s2ghRewrite{to: u}}
	return s
}

// s2ghPost posts one client request and returns the status code.
func s2ghPost(t *testing.T, s *Server, path, body string, hdr map[string]string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	s.Handler().ServeHTTP(rec, req)
	return rec.Code
}

const s2ghReply = "data: {\"id\":\"c1\",\"model\":\"m1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func TestS2GHDeveloperRoleBecomesSystemUpstream(t *testing.T) {
	f := &fake{t: t, reply: s2ghReply}
	s := s2ghServer(t, f, "")
	body := `{"model":"fake/m1","messages":[{"role":"developer","content":"be terse"},{"role":"user","content":"hi"}],"max_tokens":16}`
	if code := s2ghPost(t, s, "/v1/chat/completions", body, nil); code != 200 {
		t.Fatalf("status %d", code)
	}
	if bytes.Contains(f.got, []byte(`"developer"`)) {
		t.Errorf("developer role reached upstream: %s", f.got)
	}
	for _, want := range []string{`"role":"system"`, `"content":"be terse"`, `"content":"hi"`, `"max_tokens":16`, `"model":"m1"`} {
		if !bytes.Contains(f.got, []byte(want)) {
			t.Errorf("upstream body lacks %s: %s", want, f.got)
		}
	}
}

func TestS2GHDeveloperToSystemKeepsBodyWithoutDeveloper(t *testing.T) {
	in := []byte(`{"model":"m","messages":[{"role":"user","content":"developer"}],"n":1.50}`)
	if out := developerToSystem(in); !bytes.Equal(out, in) {
		t.Errorf("body changed without a developer message: %s", out)
	}
	in2 := []byte(`{"model":"m","messages":[{"role":"developer","content":"x"}],"n":1.50}`)
	out := developerToSystem(in2)
	if !bytes.Contains(out, []byte(`"role":"system"`)) || !bytes.Contains(out, []byte(`"n":1.50`)) {
		t.Errorf("developer rewrite lost content: %s", out)
	}
}

func TestS2GHOpenCodeHostMatch(t *testing.T) {
	for host, want := range map[string]bool{
		"opencode.ai":         true,
		"opencode.ai:443":     true,
		"zen.opencode.ai":     true,
		"notopencode.ai":      false,
		"opencode.ai.evil.io": false,
		"example.com":         false,
	} {
		if got := provider.OpenCodeHost(host); got != want {
			t.Errorf("OpenCodeHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestS2GHOpenCodeSessionPriorityAndStability(t *testing.T) {
	f := &fake{t: t, reply: s2ghReply}
	s := s2ghServer(t, f, "https://opencode.ai/zen/v1")

	chat := func(hdr map[string]string, msgs string) string {
		t.Helper()
		body := `{"model":"fake/m1","messages":[` + msgs + `]}`
		if code := s2ghPost(t, s, "/v1/chat/completions", body, hdr); code != 200 {
			t.Fatalf("status %d", code)
		}
		return f.head.Get(provider.OpenCodeSessionHeader)
	}

	// the agent's own session header is used as it is
	if got := chat(map[string]string{"x-opencode-session": "agent-1"}, `{"role":"user","content":"a"}`); got != "agent-1" {
		t.Errorf("agent session = %q, want agent-1", got)
	}
	// priority: x-opencode-session beats x-session-id; x-session-id beats session_id
	if got := chat(map[string]string{"x-opencode-session": "one", "x-session-id": "two"}, `{"role":"user","content":"a"}`); got != "one" {
		t.Errorf("priority 1 = %q", got)
	}
	if got := chat(map[string]string{"x-session-id": "two", "session_id": "three"}, `{"role":"user","content":"a"}`); got != "two" {
		t.Errorf("priority 2 = %q", got)
	}
	if got := chat(map[string]string{"x-claude-code-session-id": "cc"}, `{"role":"user","content":"a"}`); got != "cc" {
		t.Errorf("claude session = %q", got)
	}

	// without one, the first user message decides: stable across turns, distinct across conversations
	turn1 := chat(nil, `{"role":"user","content":"first question"}`)
	turn2 := chat(nil, `{"role":"user","content":"first question"},{"role":"assistant","content":"answer"},{"role":"user","content":"follow-up"}`)
	other := chat(nil, `{"role":"user","content":"another conversation"}`)
	if !strings.HasPrefix(turn1, "magpie-") || len(turn1) <= len("magpie-") {
		t.Fatalf("generated session = %q", turn1)
	}
	if turn1 != turn2 {
		t.Errorf("session changed across turns: %q vs %q", turn1, turn2)
	}
	if turn1 == other {
		t.Errorf("different conversations share session %q", turn1)
	}
}

func TestS2GHOpenCodeSessionResponsesInput(t *testing.T) {
	f := &fake{t: t, reply: s2ghReply}
	s := s2ghServer(t, f, "https://opencode.ai/zen/v1")

	// a Responses client against a Chat-only provider goes through translation
	if code := s2ghPost(t, s, "/v1/responses", `{"model":"fake/m1","input":"hello there"}`, nil); code != 200 {
		t.Fatalf("string input: status %d", code)
	}
	strSid := f.head.Get(provider.OpenCodeSessionHeader)
	if !strings.HasPrefix(strSid, "magpie-") {
		t.Fatalf("string input session = %q", strSid)
	}

	arr := `{"model":"fake/m1","input":[{"role":"user","content":[{"type":"input_text","text":"hello there"}]},{"role":"assistant","content":"ok"},{"role":"user","content":"again"}]}`
	if code := s2ghPost(t, s, "/v1/responses", arr, nil); code != 200 {
		t.Fatalf("array input: status %d", code)
	}
	if got := f.head.Get(provider.OpenCodeSessionHeader); got != strSid {
		t.Errorf("array input session = %q, want %q (same first user message)", got, strSid)
	}
}

func TestS2GHOtherProviderGetsNoSessionHeader(t *testing.T) {
	f := &fake{t: t, reply: s2ghReply}
	s := s2ghServer(t, f, "")
	body := `{"model":"fake/m1","messages":[{"role":"user","content":"hi"}]}`
	if code := s2ghPost(t, s, "/v1/chat/completions", body, map[string]string{"x-opencode-session": "leak"}); code != 200 {
		t.Fatalf("status %d", code)
	}
	if v, ok := f.head["X-Opencode-Session"]; ok {
		t.Errorf("non-OpenCode upstream got x-opencode-session: %v", v)
	}
}
