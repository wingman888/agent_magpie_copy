package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	want := map[string][3]string{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen"},
	}
	for id, ends := range want {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Chat != ends[0] || pr.Responses != ends[1] || pr.Anthropic != ends[2] {
			t.Errorf("%s endpoints: %q %q %q", id, pr.Chat, pr.Responses, pr.Anthropic)
		}
		if pr.KeysURL != "https://opencode.ai/auth" {
			t.Errorf("%s keys url %q", id, pr.KeysURL)
		}
		p, err := FromPreset(id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.IsOpenCode() {
			t.Errorf("%s should be an OpenCode provider", id)
		}
	}
	if Preset("opencode-go").Website != "https://opencode.ai/docs/go" || Preset("opencode-zen").Website != "https://opencode.ai/docs/zen" {
		t.Error("website urls")
	}
	listed := map[string]bool{}
	for _, p := range Presets() {
		listed[p.ID] = true
	}
	if !listed["opencode-go"] || !listed["opencode-zen"] {
		t.Error("presets list lacks the OpenCode presets")
	}
}

func TestIsOpenCode(t *testing.T) {
	cases := map[string]bool{
		"https://opencode.ai/zen/v1":      true,
		"https://sub.opencode.ai/v1":      true,
		"https://OPENCODE.AI/zen/go/v1":   true,
		"https://notopencode.ai/v1":       false,
		"https://opencode.ai.evil.com/v1": false,
		"https://api.deepseek.com/v1":     false,
	}
	for base, want := range cases {
		p := Provider{ID: "x", Name: "x", Chat: base, Key: "k"}
		if got := p.IsOpenCode(); got != want {
			t.Errorf("IsOpenCode(%s) = %v, want %v", base, got, want)
		}
	}
	if !IsOpenCodeHost("opencode.ai") || IsOpenCodeHost("notopencode.ai") {
		t.Error("IsOpenCodeHost")
	}
}

func TestOpenCodeSessionFromAgentHeaders(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	order := []string{"x-opencode-session", "x-session-affinity", "x-session-id", "session_id", "session-id", "x-claude-code-session-id"}
	for i, h := range order {
		in := http.Header{}
		// a lower-priority header is also present; the earlier one must win
		in.Set(h, "agent-"+h)
		for _, later := range order[i+1:] {
			in.Set(later, "later")
		}
		if got := OpenCodeSession(in, body); got != "agent-"+h {
			t.Errorf("%s: got %q", h, got)
		}
	}
	in := http.Header{}
	in.Set("x-session-id", "  ")
	in.Set("session-id", "fallback")
	if got := OpenCodeSession(in, body); got != "fallback" {
		t.Errorf("blank header should be skipped, got %q", got)
	}
}

func TestOpenCodeSessionStableWithinConversation(t *testing.T) {
	first := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"write a parser"}]}`
	turn2 := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"write a parser"},{"role":"assistant","content":"ok"},{"role":"user","content":"now test it"}]}`
	other := `{"messages":[{"role":"user","content":"something else"}]}`
	a := OpenCodeSession(nil, []byte(first))
	if !strings.HasPrefix(a, "magpie-") || len(a) <= len("magpie-") {
		t.Fatalf("session %q", a)
	}
	if b := OpenCodeSession(nil, []byte(turn2)); b != a {
		t.Errorf("later turn changed session: %q vs %q", b, a)
	}
	if c := OpenCodeSession(nil, []byte(other)); c == a {
		t.Errorf("different conversation shares session %q", a)
	}

	// Responses: input as a bare string, then as an item list
	str := `{"input":"write a parser"}`
	list := `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"write a parser"}]},{"type":"message","role":"assistant","content":"ok"},{"type":"message","role":"user","content":"more"}]}`
	s1 := OpenCodeSession(nil, []byte(str))
	if !strings.HasPrefix(s1, "magpie-") {
		t.Fatalf("responses string session %q", s1)
	}
	if s2 := OpenCodeSession(nil, []byte(list)); s2 != s1 {
		t.Errorf("responses list session %q != string session %q", s2, s1)
	}
	if s3 := OpenCodeSession(nil, []byte(`{"input":"different"}`)); s3 == s1 {
		t.Error("different responses conversations share a session")
	}
}

// captureTransport records the request probe sends instead of dialing out.
type captureTransport struct{ req *http.Request }

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	io.Copy(io.Discard, r.Body)
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
}

func TestProbeSendsOpenCodeSessionOnlyToOpenCode(t *testing.T) {
	ct := &captureTransport{}
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = ct
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)

	oc := Provider{ID: "oc", Name: "OC", Chat: "https://opencode.ai/zen/go/v1", Key: "k"}
	probe(context.Background(), oc, Chat, oc.Chat+"/chat/completions", body, "m")
	if ct.req == nil || !strings.HasPrefix(ct.req.Header.Get(OpenCodeSessionHeader), "magpie-") {
		t.Fatalf("OpenCode probe lacks session header: %v", ct.req)
	}

	ds := Provider{ID: "ds", Name: "DS", Chat: "https://api.deepseek.com/v1", Key: "k"}
	ct.req = nil
	probe(context.Background(), ds, Chat, ds.Chat+"/chat/completions", body, "m")
	if ct.req == nil || ct.req.Header.Get(OpenCodeSessionHeader) != "" {
		t.Fatalf("non-OpenCode probe must not carry the session header")
	}
}
