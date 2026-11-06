package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestIsOpenCodeL3x(t *testing.T) {
	cases := []struct {
		p    Provider
		want bool
	}{
		{Provider{Chat: "https://opencode.ai/zen/go/v1"}, true},
		{Provider{Anthropic: "https://opencode.ai/zen"}, true},
		{Provider{Responses: "https://api.opencode.ai/v1"}, true},
		{Provider{Chat: "https://OPENCODE.ai/zen/v1"}, true},
		{Provider{Chat: "https://notopencode.ai/v1"}, false},
		{Provider{Chat: "https://opencode.ai.example.com/v1"}, false},
		{Provider{Chat: "https://api.deepseek.com/v1"}, false},
		{Provider{}, false},
	}
	for _, c := range cases {
		if got := c.p.IsOpenCode(); got != c.want {
			t.Errorf("IsOpenCode(%+v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestOpenCodeSessionAgentHeaderWinsL3x(t *testing.T) {
	order := []string{"x-opencode-session", "x-session-affinity", "x-session-id", "session_id", "session-id", "x-claude-code-session-id"}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	h := http.Header{}
	// add headers from lowest to highest priority: after each one, it must be
	// the value used, since it now outranks everything set before it
	for i := len(order) - 1; i >= 0; i-- {
		h.Set(order[i], "sess-"+order[i])
		if got := OpenCodeSession(h, body); got != "sess-"+order[i] {
			t.Errorf("with %s set (priority %d): got %q", order[i], i, got)
		}
	}
	// empty values are skipped
	e := http.Header{}
	e.Set("x-opencode-session", "  ")
	e.Set("x-session-id", "real")
	if got := OpenCodeSession(e, body); got != "real" {
		t.Errorf("blank header not skipped: %q", got)
	}
}

func TestOpenCodeSessionFallbackStablePerConversationL3x(t *testing.T) {
	turn1 := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"fix the bug"}]}`)
	turn2 := []byte(`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"fix the bug"},{"role":"assistant","content":"ok"},{"role":"user","content":"thanks"}]}`)
	other := []byte(`{"model":"m","messages":[{"role":"user","content":"write a poem"}]}`)
	a := OpenCodeSession(nil, turn1)
	if !strings.HasPrefix(a, "magpie-") || len(a) <= len("magpie-") {
		t.Fatalf("fallback id %q", a)
	}
	if b := OpenCodeSession(nil, turn2); b != a {
		t.Errorf("later turn changed id: %q vs %q", b, a)
	}
	if c := OpenCodeSession(nil, other); c == a {
		t.Errorf("different conversation shares id %q", a)
	}
}

func TestOpenCodeSessionResponsesAndContentPartsL3x(t *testing.T) {
	str := []byte(`{"model":"m","input":"first question"}`)
	arr := []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"first question"}]},{"role":"assistant","content":"x"},{"role":"user","content":"second"}]}`)
	parts := []byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"first question"}]}]}`)
	other := []byte(`{"model":"m","input":"something else"}`)
	a := OpenCodeSession(nil, str)
	if b := OpenCodeSession(nil, arr); b != a {
		t.Errorf("responses array form differs: %q vs %q", b, a)
	}
	if b := OpenCodeSession(nil, parts); b != a {
		t.Errorf("content-part form differs: %q vs %q", b, a)
	}
	if o := OpenCodeSession(nil, other); o == a {
		t.Errorf("different input shares id")
	}
}

func TestOpenCodePresetsL3x(t *testing.T) {
	cases := map[string]PresetDef{
		"opencode-go": {Name: "OpenCode Go", Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1",
			Anthropic: "https://opencode.ai/zen/go", Website: "https://opencode.ai/docs/go", KeysURL: "https://opencode.ai/auth"},
		"opencode-zen": {Name: "OpenCode Zen", Chat: "https://opencode.ai/zen/v1", Responses: "https://opencode.ai/zen/v1",
			Anthropic: "https://opencode.ai/zen", Website: "https://opencode.ai/docs/zen", KeysURL: "https://opencode.ai/auth"},
	}
	for id, want := range cases {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Name != want.Name || pr.Chat != want.Chat || pr.Responses != want.Responses || pr.Anthropic != want.Anthropic ||
			pr.Website != want.Website || pr.KeysURL != want.KeysURL {
			t.Errorf("preset %s = %+v", id, *pr)
		}
	}
}
