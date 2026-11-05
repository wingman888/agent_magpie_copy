package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	cases := map[string]struct{ chat, responses, anthropic, website string }{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go", "https://opencode.ai/docs/go"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen", "https://opencode.ai/docs/zen"},
	}
	for id, want := range cases {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Chat != want.chat || pr.Responses != want.responses || pr.Anthropic != want.anthropic {
			t.Errorf("%s endpoints: %q %q %q", id, pr.Chat, pr.Responses, pr.Anthropic)
		}
		if pr.Website != want.website || pr.KeysURL != "https://opencode.ai/auth" {
			t.Errorf("%s website/keys: %q %q", id, pr.Website, pr.KeysURL)
		}
		p, err := FromPreset(id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.IsOpenCode() {
			t.Errorf("%s should be an OpenCode provider", id)
		}
	}
	if Preset("opencode-go").Name != "OpenCode Go" {
		t.Errorf("name %q", Preset("opencode-go").Name)
	}
}

func TestIsOpenCode(t *testing.T) {
	for _, tc := range []struct {
		base string
		want bool
	}{
		{"https://opencode.ai/zen/v1", true},
		{"https://api.opencode.ai/v1", true},
		{"https://OPENCODE.AI:443/v1", true},
		{"https://notopencode.ai/v1", false},
		{"https://opencode.ai.evil.example/v1", false},
		{"https://api.deepseek.com/v1", false},
	} {
		p := Provider{ID: "x", Chat: tc.base}
		if got := p.IsOpenCode(); got != tc.want {
			t.Errorf("IsOpenCode(%s) = %v, want %v", tc.base, got, tc.want)
		}
	}
}

func openCodeReq(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestOpenCodeSessionAgentHeaderWins(t *testing.T) {
	in := http.Header{}
	in.Set("x-session-id", "second")
	in.Set("X-Opencode-Session", "first")
	req := openCodeReq(t, "https://opencode.ai/zen/go/v1/chat/completions")
	ApplyOpenCodeSession(req, in, []byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if got := req.Header.Get("x-opencode-session"); got != "first" {
		t.Errorf("session = %q, want first", got)
	}
}

func TestOpenCodeSessionSkipsEmptyAgentHeader(t *testing.T) {
	in := http.Header{}
	in.Set("x-opencode-session", "   ")
	in.Set("session_id", "abc")
	if got := OpenCodeSession(in, nil); got != "abc" {
		t.Errorf("session = %q, want abc", got)
	}
}

func TestOpenCodeSessionStableFallback(t *testing.T) {
	chatTurn1 := `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"write a parser"}]}`
	chatTurn2 := `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"write a parser"},{"role":"assistant","content":"ok"},{"role":"user","content":"now tests"}]}`
	other := `{"model":"m","messages":[{"role":"user","content":"something else"}]}`
	respTurn1 := `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"write a parser"}]}]}`
	respTurn2 := `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"write a parser"}]},{"type":"function_call_output","call_id":"c","output":"x"}]}`
	respString := `{"model":"m","input":"write a parser"}`
	anthropicTurn := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"write a parser"}]}]}`

	id := func(body string) string { return OpenCodeSession(nil, []byte(body)) }
	first := id(chatTurn1)
	if !strings.HasPrefix(first, "magpie-") || len(first) <= len("magpie-") {
		t.Fatalf("fallback id %q", first)
	}
	if id(chatTurn2) != first {
		t.Errorf("later Chat turn changed the session id")
	}
	if id(other) == first {
		t.Errorf("different conversation shares the session id")
	}
	if id(respTurn1) != first || id(respTurn2) != first {
		t.Errorf("Responses input does not match the Chat id: %q %q vs %q", id(respTurn1), id(respTurn2), first)
	}
	if id(respString) != first {
		t.Errorf("string input does not match the Chat id")
	}
	if id(anthropicTurn) != first {
		t.Errorf("text-part content does not match the Chat id")
	}
}

func TestApplyOpenCodeSessionOnlyOpenCodeHosts(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	req := openCodeReq(t, "https://api.deepseek.com/v1/chat/completions")
	ApplyOpenCodeSession(req, http.Header{"X-Session-Id": {"s"}}, body)
	if _, ok := req.Header["X-Opencode-Session"]; ok || req.Header.Get("x-opencode-session") != "" {
		t.Errorf("non-OpenCode host received the session header")
	}
	req = openCodeReq(t, "https://notopencode.ai/v1/chat/completions")
	ApplyOpenCodeSession(req, nil, body)
	if req.Header.Get("x-opencode-session") != "" {
		t.Errorf("notopencode.ai received the session header")
	}
	req = openCodeReq(t, "https://opencode.ai/zen/v1/responses")
	ApplyOpenCodeSession(req, nil, body)
	if !strings.HasPrefix(req.Header.Get("x-opencode-session"), "magpie-") {
		t.Errorf("OpenCode host without header: %q", req.Header.Get("x-opencode-session"))
	}
}
