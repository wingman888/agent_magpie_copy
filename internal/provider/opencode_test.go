package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// ocCapture answers every request 200 and remembers the session header.
type ocCapture struct {
	session string
	seen    []string // method url session, one per request
}

func (c *ocCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	c.session = r.Header.Get(OpenCodeSessionHeader)
	c.seen = append(c.seen, r.Method+" "+r.URL.String()+" ["+c.session+"]")
	if r.Body != nil {
		io.Copy(io.Discard, r.Body)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
}

func TestOCIsOpenCodeHosts(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"https://opencode.ai/zen/v1", true},
		{"https://opencode.ai/zen/go", true},
		{"https://api.opencode.ai/v1", true},
		{"https://OPENCODE.AI/v1", true},
		{"https://notopencode.ai/v1", false},
		{"https://opencode.ai.evil.com/v1", false},
		{"https://api.openai.com/v1", false},
		{"http://localhost:11434/v1", false},
	}
	for _, c := range cases {
		p := Provider{ID: "x", Chat: c.base}
		if got := p.IsOpenCode(); got != c.want {
			t.Errorf("IsOpenCode(%s) = %v, want %v", c.base, got, c.want)
		}
	}
	if (Provider{ID: "x"}).IsOpenCode() {
		t.Error("provider with no base URL reported as OpenCode")
	}
}

func TestOCPresetEndpoints(t *testing.T) {
	want := map[string][3]string{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen"},
	}
	for id, eps := range want {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Chat != eps[0] || pr.Responses != eps[1] || pr.Anthropic != eps[2] {
			t.Errorf("%s endpoints = %s %s %s", id, pr.Chat, pr.Responses, pr.Anthropic)
		}
		if pr.KeysURL != "https://opencode.ai/auth" {
			t.Errorf("%s keys url %s", id, pr.KeysURL)
		}
		p, err := FromPreset(id)
		if err != nil || !p.IsOpenCode() {
			t.Errorf("FromPreset(%s) not an OpenCode provider: %v", id, err)
		}
	}
	if pr := Preset("opencode-go"); pr.Website != "https://opencode.ai/docs/go" || pr.Name != "OpenCode Go" {
		t.Errorf("opencode-go = %+v", pr)
	}
	if pr := Preset("opencode-zen"); pr.Website != "https://opencode.ai/docs/zen" || pr.Name != "OpenCode Zen" {
		t.Errorf("opencode-zen = %+v", pr)
	}
}

func TestOCSessionValueRules(t *testing.T) {
	h := http.Header{}
	h.Set("x-session-affinity", "aff")
	h.Set("x-claude-code-session-id", "cc")
	if got := OpenCodeSession(h, nil); got != "aff" {
		t.Errorf("affinity header: got %q", got)
	}
	empty := http.Header{}
	empty.Set("x-opencode-session", "  ")
	empty.Set("session-id", "sid")
	if got := OpenCodeSession(empty, nil); got != "sid" {
		t.Errorf("blank header should be skipped: got %q", got)
	}

	chat := []byte(`{"messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"first"}]},{"role":"user","content":"second"}]}`)
	a := OpenCodeSession(nil, chat)
	b := OpenCodeSession(nil, []byte(`{"messages":[{"role":"user","content":"first"}]}`))
	if a != b || !strings.HasPrefix(a, "magpie-") {
		t.Errorf("chat parts vs string: %q vs %q", a, b)
	}
	if c := OpenCodeSession(nil, []byte(`{"messages":[{"role":"user","content":"other"}]}`)); c == a {
		t.Error("different first messages gave the same session")
	}
}

// Test sends an OpenCode probe with the session header; the transport keeps
// it off the network.
func TestOCProbeSendsSessionHeader(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rec := &ocCapture{}
	old := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: rec}
	t.Cleanup(func() { http.DefaultClient = old })

	p := Provider{ID: "oc", Name: "OC", Key: "k", Chat: "https://opencode.ai/zen/v1", Models: []string{"m1"}}
	body := []byte(`{"model":"m1","messages":[{"role":"user","content":"hi"}],"max_tokens":16}`)
	pr := probe(context.Background(), p, Chat, p.Chat+"/chat/completions", body, "m1")
	if !pr.OK {
		t.Fatalf("probe %+v; requests %v", pr, rec.seen)
	}
	if !strings.HasPrefix(rec.session, "magpie-") {
		t.Errorf("probe sent %q as %s; requests %v", rec.session, OpenCodeSessionHeader, rec.seen)
	}

	// a non-OpenCode provider must not get the header
	rec.session = ""
	q := Provider{ID: "other", Name: "Other", Key: "k", Chat: "https://api.example.com/v1", Models: []string{"m1"}}
	probe(context.Background(), q, Chat, q.Chat+"/chat/completions", body, "m1")
	if rec.session != "" {
		t.Errorf("non-OpenCode probe sent %q; requests %v", rec.session, rec.seen)
	}
}
