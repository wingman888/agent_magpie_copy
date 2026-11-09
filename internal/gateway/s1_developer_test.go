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

// s1Transport records the request that would have gone upstream.
type s1Transport struct {
	req  *http.Request
	body []byte
}

func (t *s1Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.req = r
	t.body, _ = io.ReadAll(r.Body)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

func s1Server(t *testing.T) (*Server, *s1Transport) {
	t.Helper()
	tr := &s1Transport{}
	s := New()
	s.client = &http.Client{Transport: tr}
	return s, tr
}

func TestS1DeveloperToSystemRewrites(t *testing.T) {
	in := []byte(`{"model":"deepseek-reasoner","temperature":0.2,"messages":[{"role":"developer","content":"be <brief> & kind"},{"role":"user","content":"hi"}]}`)
	out := developerToSystem(in)
	var got struct {
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
		Messages    []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Model != "deepseek-reasoner" || got.Temperature != 0.2 {
		t.Errorf("other fields changed: %+v", got)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "be <brief> & kind" {
		t.Errorf("developer not rewritten, content kept: %+v", got.Messages)
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "hi" {
		t.Errorf("user message changed: %+v", got.Messages[1])
	}
	if !bytes.Contains(out, []byte("<brief>")) {
		t.Error("HTML characters were escaped")
	}
}

func TestS1DeveloperToSystemUnchangedWithoutDeveloper(t *testing.T) {
	for _, in := range []string{
		`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`,
		`{"model":"m",  "messages" : [ {"role":"user","content":"u"} ] }`,
		`{"model":"m","input":"no messages key"}`,
		`not json`,
	} {
		if out := developerToSystem([]byte(in)); !bytes.Equal(out, []byte(in)) {
			t.Errorf("body changed without developer message:\n in: %s\nout: %s", in, out)
		}
	}
}

func TestS1ForwardChatRewritesDeveloperOnlyForChat(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"developer","content":"d"},{"role":"user","content":"u"}]}`)

	s, tr := s1Server(t)
	ds := provider.Provider{ID: "ds", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "sk"}
	if _, err := s.forward(context.Background(), ds, provider.Chat, "/chat/completions", body, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(tr.body), `"developer"`) || !strings.Contains(string(tr.body), `"system"`) {
		t.Errorf("chat upstream should get system: %s", tr.body)
	}
	if tr.req.Header.Get("x-opencode-session") != "" {
		t.Error("non-OpenCode provider got the OpenCode session header")
	}

	// Responses upstream: developer roles are left alone (the rewrite is for Chat Completions)
	if _, err := s.forward(context.Background(), ds, provider.Responses, "/responses", body, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(tr.body), `"developer"`) {
		t.Errorf("responses upstream body was rewritten: %s", tr.body)
	}
}

func TestS1ForwardOpenCodeSessionHeader(t *testing.T) {
	chat := []byte(`{"model":"m","messages":[{"role":"user","content":"first"}]}`)
	chatLater := []byte(`{"model":"m","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"a"},{"role":"user","content":"second"}]}`)
	responses := []byte(`{"model":"m","input":"first"}`)

	s, tr := s1Server(t)
	oc := provider.Provider{ID: "oc", Name: "OpenCode Go", Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1", Key: "sk"}

	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", chat, http.Header{}); err != nil {
		t.Fatal(err)
	}
	first := tr.req.Header.Get("x-opencode-session")
	if !strings.HasPrefix(first, "magpie-") {
		t.Fatalf("want magpie- session, got %q", first)
	}
	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", chatLater, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if got := tr.req.Header.Get("x-opencode-session"); got != first {
		t.Errorf("later turn session %q != %q", got, first)
	}
	if _, err := s.forward(context.Background(), oc, provider.Responses, "/responses", responses, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if got := tr.req.Header.Get("x-opencode-session"); got != first {
		t.Errorf("responses session %q != chat session %q", got, first)
	}

	// the agent's own session id is used as-is
	in := http.Header{}
	in.Set("X-Session-Id", "agent-session-1")
	if _, err := s.forward(context.Background(), oc, provider.Chat, "/chat/completions", chat, in); err != nil {
		t.Fatal(err)
	}
	if got := tr.req.Header.Get("x-opencode-session"); got != "agent-session-1" {
		t.Errorf("agent session not used: %q", got)
	}

	// notopencode.ai is not OpenCode
	fake := provider.Provider{ID: "f", Name: "F", Chat: "https://notopencode.ai/v1", Key: "sk"}
	if _, err := s.forward(context.Background(), fake, provider.Chat, "/chat/completions", chat, http.Header{}); err != nil {
		t.Fatal(err)
	}
	if tr.req.Header.Get("x-opencode-session") != "" {
		t.Error("notopencode.ai got the header")
	}
}
