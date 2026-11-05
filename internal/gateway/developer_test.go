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

// captureTransport records upstream requests instead of sending them.
type captureTransport struct {
	reqs []*http.Request
	bods [][]byte
}

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	c.reqs = append(c.reqs, r)
	c.bods = append(c.bods, b)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
}

func forwardCapture(t *testing.T, p provider.Provider, to provider.Protocol, body string, in http.Header) (*http.Request, []byte) {
	t.Helper()
	ct := &captureTransport{}
	s := New()
	s.client = &http.Client{Transport: ct}
	res, err := s.forward(context.Background(), p, to, pathOf(to), []byte(body), in)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(ct.reqs) != 1 {
		t.Fatalf("want 1 upstream request, got %d", len(ct.reqs))
	}
	return ct.reqs[0], ct.bods[0]
}

func TestDeveloperRoleBecomesSystemForChat(t *testing.T) {
	in := `{"model":"deepseek-v4","stream":true,"temperature":0.2,"messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"hi"}]}`
	p := provider.Provider{ID: "deepseek", Chat: "https://api.deepseek.com/v1"}
	_, body := forwardCapture(t, p, provider.Chat, in, nil)

	var got struct {
		Model       string `json:"model"`
		Stream      bool   `json:"stream"`
		Temperature float64
		Messages    []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "deepseek-v4" || !got.Stream || got.Temperature != 0.2 {
		t.Errorf("other fields changed: %s", body)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[0].Content != "be brief" ||
		got.Messages[1].Role != "user" || got.Messages[1].Content != "hi" {
		t.Errorf("messages = %+v, want system/user", got.Messages)
	}
}

func TestBodyWithoutDeveloperUnchanged(t *testing.T) {
	in := `{"model":"m",  "messages" : [ {"role":"system","content":"x"} ] }`
	if got := developerToSystem([]byte(in)); string(got) != in {
		t.Errorf("body changed without developer message: %s", got)
	}
	p := provider.Provider{ID: "x", Chat: "https://api.example.com/v1"}
	_, body := forwardCapture(t, p, provider.Chat, in, nil)
	if string(body) != in {
		t.Errorf("forwarded body changed: %s", body)
	}
}

func TestDeveloperRewriteOnlyForChat(t *testing.T) {
	in := `{"model":"m","input":[{"role":"developer","content":"x"}]}`
	p := provider.Provider{ID: "x", Responses: "https://api.example.com/v1"}
	_, body := forwardCapture(t, p, provider.Responses, in, nil)
	if string(body) != in {
		t.Errorf("responses body rewritten: %s", body)
	}
}

func TestOpenCodeSessionHeaderSentOnlyToOpenCode(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	oc := provider.Provider{ID: "opencode-go", Chat: "https://opencode.ai/zen/go/v1"}

	req, _ := forwardCapture(t, oc, provider.Chat, body, nil)
	got := req.Header.Get("x-opencode-session")
	if !strings.HasPrefix(got, "magpie-") {
		t.Fatalf("missing derived session header, got %q", got)
	}
	again, _ := forwardCapture(t, oc, provider.Chat, body, nil)
	if again.Header.Get("x-opencode-session") != got {
		t.Errorf("same conversation gave a different session")
	}

	in := http.Header{}
	in.Set("X-Session-Id", "agent-sess")
	req, _ = forwardCapture(t, oc, provider.Chat, body, in)
	if v := req.Header.Get("x-opencode-session"); v != "agent-sess" {
		t.Errorf("agent session = %q, want agent-sess", v)
	}

	other := provider.Provider{ID: "deepseek", Chat: "https://api.deepseek.com/v1"}
	req, _ = forwardCapture(t, other, provider.Chat, body, in)
	if v := req.Header.Get("x-opencode-session"); v != "" {
		t.Errorf("non-OpenCode provider received x-opencode-session=%q", v)
	}
}

func TestOpenCodeSessionResponsesRequest(t *testing.T) {
	body := `{"model":"m","input":"hi"}`
	oc := provider.Provider{ID: "opencode-zen", Responses: "https://opencode.ai/zen/v1"}
	req, _ := forwardCapture(t, oc, provider.Responses, body, nil)
	if !strings.HasPrefix(req.Header.Get("x-opencode-session"), "magpie-") {
		t.Errorf("responses request lacks derived session header")
	}
}

func TestDeveloperToSystemKeepsOtherMessages(t *testing.T) {
	in := `{"messages":[{"role":"developer","content":[{"type":"text","text":"a"}]},{"role":"user","content":"b"}]}`
	out := developerToSystem([]byte(in))
	if bytes.Contains(out, []byte(`"developer"`)) || !bytes.Contains(out, []byte(`"system"`)) {
		t.Errorf("rewrite failed: %s", out)
	}
	if !bytes.Contains(out, []byte(`"text":"a"`)) || !bytes.Contains(out, []byte(`"content":"b"`)) {
		t.Errorf("content lost: %s", out)
	}
}
