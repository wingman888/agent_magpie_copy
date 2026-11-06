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

func TestDeveloperToSystemRewritesRoleOnly(t *testing.T) {
	in := []byte(`{"model":"m","temperature":0.10,"messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`)
	out := developerToSystem(in)
	var v struct {
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
		Messages    []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v.Model != "m" || v.Temperature != 0.1 || len(v.Messages) != 2 {
		t.Fatalf("other fields changed: %s", out)
	}
	if v.Messages[0].Role != "system" || v.Messages[0].Content != "rules" || v.Messages[1].Role != "user" || v.Messages[1].Content != "hi" {
		t.Errorf("messages: %s", out)
	}
}

func TestDeveloperToSystemLeavesOtherBodiesAlone(t *testing.T) {
	for _, in := range []string{
		`{"model":"m",  "messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
		`{"input":"hi","model":"m"}`,
		`not json`,
	} {
		if out := developerToSystem([]byte(in)); !bytes.Equal(out, []byte(in)) {
			t.Errorf("body changed without a developer message:\n%s\n%s", in, out)
		}
	}
}

// recordTransport captures the upstream request and answers 200 with no
// network access, so OpenCode hostnames can be exercised.
type recordTransport struct {
	req  *http.Request
	body []byte
}

func (rt *recordTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.req = r
	rt.body, _ = io.ReadAll(r.Body)
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
}

func newRecordingServer() (*Server, *recordTransport) {
	s := New()
	rt := &recordTransport{}
	s.client.Transport = rt
	return s, rt
}

func TestForwardSendsOpenCodeSessionHeader(t *testing.T) {
	chatBody := []byte(`{"model":"m","messages":[{"role":"user","content":"write a parser"}]}`)
	oc := provider.Provider{ID: "oc", Name: "OC", Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1", Key: "k"}

	// no agent header: a stable magpie- id for the conversation
	s, rt := newRecordingServer()
	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", chatBody, http.Header{}); err != nil {
		t.Fatal(err)
	}
	gen := rt.req.Header.Get("x-opencode-session")
	if !strings.HasPrefix(gen, "magpie-") {
		t.Fatalf("generated session %q", gen)
	}
	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", chatBody, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if again := rt.req.Header.Get("x-opencode-session"); again != gen {
		t.Errorf("same conversation got %q then %q", gen, again)
	}

	// Responses form of the same conversation
	respBody := []byte(`{"model":"m","input":"write a parser"}`)
	if _, err := s.forward(context.Background(), oc, provider.Responses, "/responses", respBody, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if got := rt.req.Header.Get("x-opencode-session"); got != gen {
		t.Errorf("responses session %q != chat session %q", got, gen)
	}

	// the agent's own session header is used as is
	in := http.Header{}
	in.Set("x-session-id", "agent-sess-1")
	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", chatBody, in); err != nil {
		t.Fatal(err)
	}
	if got := rt.req.Header.Get("x-opencode-session"); got != "agent-sess-1" {
		t.Errorf("agent session %q", got)
	}
}

func TestForwardOtherProvidersGetNoSessionHeader(t *testing.T) {
	s, rt := newRecordingServer()
	ds := provider.Provider{ID: "ds", Name: "DS", Chat: "https://api.deepseek.com/v1", Key: "k"}
	in := http.Header{}
	in.Set("x-session-id", "agent-sess-1")
	if _, err := s.forward(context.Background(), ds, provider.Chat, "/chat/completions", []byte(`{"messages":[]}`), in); err != nil {
		t.Fatal(err)
	}
	if v := rt.req.Header.Values("x-opencode-session"); len(v) != 0 {
		t.Errorf("non-OpenCode upstream got the header: %v", v)
	}
}

func TestForwardRewritesDeveloperForChatOnly(t *testing.T) {
	s, rt := newRecordingServer()
	ds := provider.Provider{ID: "ds", Name: "DS", Chat: "https://api.deepseek.com/v1", Responses: "https://api.deepseek.com/v1", Key: "k"}
	body := []byte(`{"model":"m","messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`)

	if _, err := s.forward(context.Background(), ds, provider.Chat, "/chat/completions", body, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rt.body, []byte(`"role":"system"`)) || bytes.Contains(rt.body, []byte(`"developer"`)) {
		t.Errorf("chat upstream body: %s", rt.body)
	}

	if _, err := s.forward(context.Background(), ds, provider.Responses, "/responses", body, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(rt.body, []byte(`"developer"`)) {
		t.Errorf("responses upstream body must keep developer: %s", rt.body)
	}
}
