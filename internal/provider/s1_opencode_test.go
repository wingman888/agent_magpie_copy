package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestS1PresetsOpenCode(t *testing.T) {
	ids := map[string]bool{}
	for _, p := range Presets() {
		ids[p.ID] = true
	}
	if !ids["opencode-go"] || !ids["opencode-zen"] {
		t.Fatalf("presets missing opencode entries: %v", ids)
	}
	cases := []struct {
		id, chat, responses, anthropic, website string
	}{
		{"opencode-go", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go", "https://opencode.ai/docs/go"},
		{"opencode-zen", "https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen", "https://opencode.ai/docs/zen"},
	}
	for _, c := range cases {
		pr := Preset(c.id)
		if pr == nil {
			t.Fatalf("%s: no preset", c.id)
		}
		if pr.Chat != c.chat || pr.Responses != c.responses || pr.Anthropic != c.anthropic || pr.Website != c.website {
			t.Errorf("%s: endpoints %+v", c.id, pr)
		}
		if pr.KeysURL != "https://opencode.ai/auth" {
			t.Errorf("%s: keys url %q", c.id, pr.KeysURL)
		}
		p, err := FromPreset(c.id)
		if err != nil {
			t.Fatal(err)
		}
		p.Key = "sk-test"
		if !p.IsOpenCode() {
			t.Errorf("%s: IsOpenCode false", c.id)
		}
	}
}

func TestS1IsOpenCodeHosts(t *testing.T) {
	yes := []string{"https://opencode.ai/zen/v1", "https://api.opencode.ai/v1", "https://OPENCODE.AI:443/x"}
	no := []string{"https://notopencode.ai/v1", "https://opencode.ai.example.com/v1", "https://api.deepseek.com/v1", "https://example.com/opencode.ai/v1"}
	for _, u := range yes {
		if !(Provider{Chat: u}).IsOpenCode() {
			t.Errorf("%s: want IsOpenCode", u)
		}
	}
	for _, u := range no {
		if (Provider{Chat: u}).IsOpenCode() {
			t.Errorf("%s: want not IsOpenCode", u)
		}
	}
	if (Provider{}).IsOpenCode() {
		t.Error("empty provider is not OpenCode")
	}
}

func TestS1OpenCodeSessionHeaderOrder(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	in := http.Header{}
	in.Set("x-session-id", "from-x-session-id")
	in.Set("session-id", "from-session-id")
	if got := OpenCodeSession(in, body); got != "from-x-session-id" {
		t.Errorf("got %q, want x-session-id", got)
	}
	in = http.Header{}
	in.Set("x-opencode-session", "ses_1")
	in.Set("x-session-affinity", "aff")
	if got := OpenCodeSession(in, body); got != "ses_1" {
		t.Errorf("got %q, want x-opencode-session first", got)
	}
	in = http.Header{}
	in.Set("x-opencode-session", "  ")
	in.Set("x-claude-code-session-id", "cc")
	if got := OpenCodeSession(in, body); got != "cc" {
		t.Errorf("empty value must be skipped, got %q", got)
	}
}

func TestS1OpenCodeSessionStableAndDistinct(t *testing.T) {
	chat1 := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"first question"}]}`)
	chat2 := []byte(`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"first question"},{"role":"assistant","content":"ans"},{"role":"user","content":"second"}]}`)
	other := []byte(`{"messages":[{"role":"user","content":"another conversation"}]}`)
	resp1 := []byte(`{"input":"first question"}`)
	resp1list := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"first question"}]}]}`)
	resp2 := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"first question"}]},{"type":"function_call_output","call_id":"c","output":"x"}]}`)

	a := OpenCodeSession(nil, chat1)
	if !strings.HasPrefix(a, "magpie-") {
		t.Fatalf("want magpie- prefix, got %q", a)
	}
	if a != OpenCodeSession(nil, chat2) {
		t.Error("later chat turns must keep the same session")
	}
	if a == OpenCodeSession(nil, other) {
		t.Error("different conversations must differ")
	}
	if a != OpenCodeSession(nil, resp1) || a != OpenCodeSession(nil, resp1list) {
		t.Error("responses input (string and list) must match the chat session of the same first message")
	}
	if a != OpenCodeSession(nil, resp2) {
		t.Error("later responses turns must keep the same session")
	}
}

func TestS1ProbeSendsOpenCodeHeader(t *testing.T) {
	var got http.Header
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = s1RoundTrip(func(r *http.Request) *http.Response {
		got = r.Header.Clone()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}
	})
	defer func() { http.DefaultClient.Transport = old }()

	oc := Provider{ID: "oc", Chat: "https://opencode.ai/zen/go/v1", Key: "sk"}
	res := probe(context.Background(), oc, Chat, oc.Chat+"/chat/completions", []byte(`{"messages":[{"role":"user","content":"hi"}]}`), "m")
	if !res.OK {
		t.Fatalf("probe failed: %+v", res)
	}
	if !strings.HasPrefix(got.Get("x-opencode-session"), "magpie-") {
		t.Errorf("opencode probe missing session header: %v", got)
	}

	other := Provider{ID: "ds", Chat: "https://api.deepseek.com/v1", Key: "sk"}
	probe(context.Background(), other, Chat, other.Chat+"/chat/completions", []byte(`{}`), "m")
	if v := got.Values("x-opencode-session"); len(v) != 0 {
		t.Errorf("other provider got the opencode header: %v", v)
	}
}

// s1RoundTrip answers requests without a network.
type s1RoundTrip func(*http.Request) *http.Response

func (f s1RoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r), nil }
