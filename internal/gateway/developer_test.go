package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestDeveloperToSystemRewritesRole(t *testing.T) {
	in := []byte(`{"model":"m","stream":true,"messages":[{"role":"developer","content":"be terse <b>&"},{"role":"user","content":"hi","name":"bob"}]}`)
	out := developerToSystem(in)

	if bytes.Contains(out, []byte(`"developer"`)) {
		t.Fatalf("developer role survived: %s", out)
	}
	var got struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
			Name    string `json:"name"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "m" || !got.Stream {
		t.Errorf("top-level fields changed: %+v", got)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != "be terse <b>&" {
		t.Errorf("rewritten message: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Name != "bob" {
		t.Errorf("user message changed: %+v", got.Messages[1])
	}
	if !strings.Contains(string(out), "<b>&") {
		t.Errorf("HTML characters were escaped: %s", out)
	}
}

func TestDeveloperToSystemNoDeveloperIsByteIdentical(t *testing.T) {
	in := []byte(`{ "model" : "m",  "messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}] }`)
	if out := developerToSystem(in); !bytes.Equal(out, in) {
		t.Errorf("body changed without a developer message:\n%s\n%s", in, out)
	}
	for _, in := range []string{`{"model":"m"}`, `not json`, `{"messages":"x"}`, `{"messages":[]}`} {
		if out := developerToSystem([]byte(in)); string(out) != in {
			t.Errorf("%s changed to %s", in, out)
		}
	}
}

func TestChatUpstreamGetsSystemRole(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"c","object":"chat.completion","choices":[]}`}
	setup(t, provider.Chat, f)
	body := `{"model":"fake/m1","messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`
	if code, out := post(t, "/v1/chat/completions", body); code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if bytes.Contains(f.got, []byte(`"developer"`)) {
		t.Errorf("upstream still got developer: %s", f.got)
	}
	if !bytes.Contains(f.got, []byte(`"role":"system"`)) || !bytes.Contains(f.got, []byte(`"content":"rules"`)) {
		t.Errorf("upstream did not get system: %s", f.got)
	}
	if !bytes.Contains(f.got, []byte(`"model":"m1"`)) {
		t.Errorf("model not rewritten: %s", f.got)
	}
}

func TestResponsesUpstreamKeepsDeveloperRole(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", reply: `{"id":"r","object":"response","output":[]}`}
	setup(t, provider.Responses, f)
	body := `{"model":"fake/m1","input":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`
	post(t, "/v1/responses", body)
	if !bytes.Contains(f.got, []byte(`"role":"developer"`)) {
		t.Errorf("Responses upstream lost developer role: %s", f.got)
	}
}

func TestUpstreamRequestOpenCodeSession(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	ctx := context.Background()

	oc := provider.Provider{ID: "go", Name: "OpenCode Go", Key: "k", Chat: "https://opencode.ai/zen/go/v1"}
	req, err := upstreamRequest(ctx, oc, provider.Chat, "/chat/completions", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	first := req.Header.Get("x-opencode-session")
	if !strings.HasPrefix(first, "magpie-") {
		t.Fatalf("no derived session on OpenCode request: %q", first)
	}
	again, _ := upstreamRequest(ctx, oc, provider.Chat, "/chat/completions", body, nil)
	if got := again.Header.Get("x-opencode-session"); got != first {
		t.Errorf("same conversation got %q then %q", first, got)
	}

	in := http.Header{}
	in.Set("X-Session-Id", "agent-sess")
	req, _ = upstreamRequest(ctx, oc, provider.Chat, "/chat/completions", body, in)
	if got := req.Header.Get("x-opencode-session"); got != "agent-sess" {
		t.Errorf("agent session not used: %q", got)
	}

	// Responses form on the same host
	resp := provider.Provider{ID: "go", Name: "OpenCode Go", Key: "k", Responses: "https://opencode.ai/zen/go/v1"}
	rbody := []byte(`{"model":"m","input":"hello"}`)
	req, _ = upstreamRequest(ctx, resp, provider.Responses, "/responses", rbody, nil)
	if got := req.Header.Get("x-opencode-session"); !strings.HasPrefix(got, "magpie-") {
		t.Errorf("Responses request: session %q", got)
	}

	other := provider.Provider{ID: "ds", Name: "DeepSeek", Key: "k", Chat: "https://api.deepseek.com/v1"}
	in = http.Header{}
	in.Set("X-Session-Id", "agent-sess")
	req, _ = upstreamRequest(ctx, other, provider.Chat, "/chat/completions", body, in)
	if _, ok := req.Header["X-Opencode-Session"]; ok {
		t.Error("non-OpenCode provider received x-opencode-session")
	}

	lookalike := provider.Provider{ID: "x", Name: "X", Key: "k", Chat: "https://notopencode.ai/v1"}
	req, _ = upstreamRequest(ctx, lookalike, provider.Chat, "/chat/completions", body, nil)
	if _, ok := req.Header["X-Opencode-Session"]; ok {
		t.Error("notopencode.ai received x-opencode-session")
	}
}
