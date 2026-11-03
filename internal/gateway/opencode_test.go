package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// recorder is a transport that keeps the last request and answers 200 with
// an empty JSON object, so forward can be exercised without a network.
type recorder struct {
	req  *http.Request
	body []byte
}

func (c *recorder) RoundTrip(r *http.Request) (*http.Response, error) {
	c.req = r
	if r.Body != nil {
		c.body, _ = io.ReadAll(r.Body)
	}
	return &http.Response{
		StatusCode: 200,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    r,
	}, nil
}

func forwardWith(t *testing.T, p provider.Provider, to provider.Protocol, body string, in http.Header) *recorder {
	t.Helper()
	rec := &recorder{}
	s := New()
	s.client = &http.Client{Transport: rec}
	res, err := s.forward(context.Background(), p, to, pathOf(to), []byte(body), in)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if rec.req == nil {
		t.Fatal("no request reached the transport")
	}
	return rec
}

func TestDeveloperToSystemRewritesOnlyRole(t *testing.T) {
	in := `{"model":"m1","stream":true,"messages":[{"role":"developer","content":"be <terse> & exact"},{"role":"user","content":"hi"}]}`
	out := developerToSystem([]byte(in))

	var got, want map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	json.Unmarshal([]byte(in), &want)
	msgs := want["messages"].([]any)
	msgs[0].(map[string]any)["role"] = "system"
	if !jsonEqual(got, want) {
		t.Errorf("rewrite changed more than the role:\n got  %s\n want %v", out, want)
	}
	if !bytes.Contains(out, []byte(`be <terse> & exact`)) {
		t.Errorf("content was escaped: %s", out)
	}
}

func TestDeveloperToSystemNoDeveloperUnchanged(t *testing.T) {
	bodies := []string{
		`{"model":"m1","messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
		`{"model":"m1",  "messages" : [ {"role":"user","content":"developer"} ]}`,
		`{"input":"no messages here"}`,
		`not json developer`,
	}
	for _, b := range bodies {
		if out := developerToSystem([]byte(b)); !bytes.Equal(out, []byte(b)) {
			t.Errorf("body changed without developer message:\n%s\n->%s", b, out)
		}
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestForwardChatRewritesDeveloperUpstream(t *testing.T) {
	p := provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Chat: "https://api.deepseek.com/v1"}
	body := `{"model":"m1","messages":[{"role":"developer","content":"d"},{"role":"user","content":"hi"}]}`
	rec := forwardWith(t, p, provider.Chat, body, nil)
	if strings.Contains(string(rec.body), `"developer"`) || !strings.Contains(string(rec.body), `"role":"system"`) {
		t.Errorf("upstream still sees developer: %s", rec.body)
	}
}

func TestForwardResponsesKeepsDeveloper(t *testing.T) {
	p := provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Responses: "https://api.deepseek.com/v1"}
	body := `{"model":"m1","input":[{"role":"developer","content":"d"}]}`
	rec := forwardWith(t, p, provider.Responses, body, nil)
	if string(rec.body) != body {
		t.Errorf("responses body altered: %s", rec.body)
	}
}

func TestForwardSendsOpenCodeSessionOnlyToOpenCode(t *testing.T) {
	body := `{"model":"m1","messages":[{"role":"user","content":"hello"}]}`
	oc := provider.Provider{ID: "go", Name: "OpenCode Go", Key: "k", Chat: "https://opencode.ai/zen/go/v1"}

	rec := forwardWith(t, oc, provider.Chat, body, nil)
	got := rec.req.Header.Get("x-opencode-session")
	if !strings.HasPrefix(got, "magpie-") {
		t.Fatalf("derived session header = %q", got)
	}

	in := http.Header{}
	in.Set("x-session-id", "agent-42")
	if got := forwardWith(t, oc, provider.Chat, body, in).req.Header.Get("x-opencode-session"); got != "agent-42" {
		t.Errorf("agent session header not used: %q", got)
	}

	other := provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Chat: "https://api.deepseek.com/v1"}
	if got := forwardWith(t, other, provider.Chat, body, in).req.Header.Get("x-opencode-session"); got != "" {
		t.Errorf("non-OpenCode upstream received session header %q", got)
	}
	lookalike := provider.Provider{ID: "n", Name: "N", Key: "k", Chat: "https://notopencode.ai/v1"}
	if got := forwardWith(t, lookalike, provider.Chat, body, nil).req.Header.Get("x-opencode-session"); got != "" {
		t.Errorf("notopencode.ai received session header %q", got)
	}
}

func TestForwardOpenCodeResponsesSession(t *testing.T) {
	oc := provider.Provider{ID: "zen", Name: "OpenCode Zen", Key: "k", Responses: "https://opencode.ai/zen/v1"}
	turn1 := `{"model":"m1","input":[{"role":"user","content":"plan it"}]}`
	turn2 := `{"model":"m1","input":[{"role":"user","content":"plan it"},{"type":"message","role":"assistant","content":"ok"},{"role":"user","content":"go"}]}`
	a := forwardWith(t, oc, provider.Responses, turn1, nil).req.Header.Get("x-opencode-session")
	b := forwardWith(t, oc, provider.Responses, turn2, nil).req.Header.Get("x-opencode-session")
	if a == "" || a != b {
		t.Errorf("responses session not stable across turns: %q vs %q", a, b)
	}
}

func TestForwardOpenCodeAnthropicSession(t *testing.T) {
	oc := provider.Provider{ID: "go", Name: "OpenCode Go", Key: "k", Anthropic: "https://opencode.ai/zen/go"}
	body := `{"model":"m1","max_tokens":8,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`
	if got := forwardWith(t, oc, provider.Anthropic, body, nil).req.Header.Get("x-opencode-session"); !strings.HasPrefix(got, "magpie-") {
		t.Errorf("anthropic upstream session = %q", got)
	}
}
