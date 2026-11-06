package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	for id, want := range map[string]struct{ chat, responses, anthropic, website string }{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go", "https://opencode.ai/docs/go"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen", "https://opencode.ai/docs/zen"},
	} {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Chat != want.chat || pr.Responses != want.responses || pr.Anthropic != want.anthropic {
			t.Fatalf("%s endpoints: %+v", id, pr)
		}
		if pr.Website != want.website || pr.KeysURL != "https://opencode.ai/auth" {
			t.Fatalf("%s links: %+v", id, pr)
		}
	}
}

func TestIsOpenCode(t *testing.T) {
	cases := map[string]bool{
		"https://opencode.ai/zen/go/v1":   true,
		"https://api.opencode.ai/v1":      true,
		"https://OPENCODE.AI/zen":         true,
		"https://notopencode.ai/v1":       false,
		"https://opencode.ai.example/v1":  false,
		"https://api.deepseek.com/v1":     false,
		"https://example.com/opencode.ai": false,
	}
	for base, want := range cases {
		p := Provider{ID: "x", Key: "k", Chat: base}
		if got := p.IsOpenCode(); got != want {
			t.Errorf("IsOpenCode(%s) = %v, want %v", base, got, want)
		}
	}
	if (Provider{ID: "x", Key: "k", Anthropic: "https://opencode.ai/zen"}).IsOpenCode() != true {
		t.Error("an Anthropic base on opencode.ai should count")
	}
}

const chatBody = `{"model":"m1","messages":[{"role":"system","content":"sys"},{"role":"user","content":"first question"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`

func TestOpenCodeSessionAgentHeaderWins(t *testing.T) {
	const u = "https://opencode.ai/zen/go/v1/chat/completions"
	for _, h := range []string{"x-opencode-session", "x-session-affinity", "x-session-id", "session_id", "session-id", "x-claude-code-session-id"} {
		in := http.Header{}
		in.Set(h, "agent-"+h)
		got, ok := OpenCodeSession(u, in, []byte(chatBody))
		if !ok || got != "agent-"+h {
			t.Errorf("%s: got %q %v", h, got, ok)
		}
	}
	// earlier names in the list win over later ones
	in := http.Header{}
	in.Set("session-id", "late")
	in.Set("x-session-id", "early")
	if got, _ := OpenCodeSession(u, in, nil); got != "early" {
		t.Errorf("order: got %q", got)
	}
	// empty values are skipped
	in = http.Header{}
	in.Set("x-opencode-session", "  ")
	in.Set("session-id", "real")
	if got, _ := OpenCodeSession(u, in, nil); got != "real" {
		t.Errorf("empty skip: got %q", got)
	}
}

func TestOpenCodeSessionDerivedIsStable(t *testing.T) {
	const u = "https://opencode.ai/zen/v1/chat/completions"
	first, ok := OpenCodeSession(u, nil, []byte(chatBody))
	if !ok || !strings.HasPrefix(first, "magpie-") {
		t.Fatalf("chat: %q %v", first, ok)
	}
	// a later turn of the same conversation: more messages, same first user message
	later := `{"model":"m1","messages":[{"role":"system","content":"sys"},{"role":"user","content":"first question"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"},{"role":"assistant","content":"x"},{"role":"user","content":"third"}]}`
	if got, _ := OpenCodeSession(u, nil, []byte(later)); got != first {
		t.Errorf("later turn changed id: %q vs %q", got, first)
	}
	// a different conversation
	other := `{"model":"m1","messages":[{"role":"user","content":"something else"}]}`
	if got, _ := OpenCodeSession(u, nil, []byte(other)); got == first {
		t.Errorf("different conversations share id %q", got)
	}
	// Responses: input as a string, and as items with content parts
	respStr := `{"model":"m1","input":"first question"}`
	respArr := `{"model":"m1","input":[{"type":"message","role":"system","content":"sys"},{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]},{"type":"message","role":"assistant","content":"ok"},{"type":"message","role":"user","content":"second"}]}`
	a, _ := OpenCodeSession(u, nil, []byte(respStr))
	b, _ := OpenCodeSession(u, nil, []byte(respArr))
	if a != first || b != first {
		t.Errorf("responses forms: string %q, items %q, want %q", a, b, first)
	}
	// Anthropic content blocks
	anth := `{"model":"m1","messages":[{"role":"user","content":[{"type":"text","text":"first question"}]}]}`
	if got, _ := OpenCodeSession(u, nil, []byte(anth)); got != first {
		t.Errorf("anthropic form: %q", got)
	}
}

func TestOpenCodeSessionOnlyForOpenCode(t *testing.T) {
	in := http.Header{}
	in.Set("x-session-id", "agent")
	for _, u := range []string{"https://api.deepseek.com/v1/chat/completions", "https://notopencode.ai/v1/chat/completions", "http://127.0.0.1:1234/v1"} {
		if got, ok := OpenCodeSession(u, in, []byte(chatBody)); ok {
			t.Errorf("%s got a session header %q", u, got)
		}
	}
}
