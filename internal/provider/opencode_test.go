package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	cases := map[string]PresetDef{
		"opencode-go": {Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1", Anthropic: "https://opencode.ai/zen/go",
			Website: "https://opencode.ai/docs/go", KeysURL: "https://opencode.ai/auth"},
		"opencode-zen": {Chat: "https://opencode.ai/zen/v1", Responses: "https://opencode.ai/zen/v1", Anthropic: "https://opencode.ai/zen",
			Website: "https://opencode.ai/docs/zen", KeysURL: "https://opencode.ai/auth"},
	}
	for id, want := range cases {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Chat != want.Chat || pr.Responses != want.Responses || pr.Anthropic != want.Anthropic ||
			pr.Website != want.Website || pr.KeysURL != want.KeysURL {
			t.Errorf("preset %s = %+v, want endpoints %+v", id, *pr, want)
		}
		p, err := FromPreset(id)
		if err != nil {
			t.Fatal(err)
		}
		if !p.IsOpenCode() {
			t.Errorf("preset %s should be an OpenCode provider", id)
		}
	}
}

func TestIsOpenCode(t *testing.T) {
	yes := []Provider{
		{Chat: "https://opencode.ai/zen/v1"},
		{Anthropic: "https://OPENCODE.AI/zen"},
		{Responses: "https://gw.opencode.ai/v1"},
		{Chat: "https://opencode.ai:443/v1"},
	}
	no := []Provider{
		{Chat: "https://notopencode.ai/v1"},
		{Chat: "https://opencode.ai.example.com/v1"},
		{Chat: "https://api.deepseek.com/v1"},
		{},
	}
	for _, p := range yes {
		if !p.IsOpenCode() {
			t.Errorf("%+v: want IsOpenCode", p)
		}
	}
	for _, p := range no {
		if p.IsOpenCode() {
			t.Errorf("%+v: want not IsOpenCode", p)
		}
	}
}

func TestOpenCodeSessionHeaderPriority(t *testing.T) {
	h := http.Header{}
	h.Set("session-id", "from-session-id")
	h.Set("x-opencode-session", "  ")
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	if got := OpenCodeSession(h, body); got != "from-session-id" {
		t.Errorf("got %q, want the first non-empty agent header", got)
	}
	h.Set("x-opencode-session", "ses_abc")
	if got := OpenCodeSession(h, body); got != "ses_abc" {
		t.Errorf("got %q, want x-opencode-session verbatim", got)
	}
}

func TestOpenCodeSessionDerivedStable(t *testing.T) {
	first := `{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"write a parser"}]}`
	later := `{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"write a parser"},{"role":"assistant","content":"ok"},{"role":"user","content":"now tests"}]}`
	other := `{"messages":[{"role":"user","content":"something else"}]}`

	a := OpenCodeSession(nil, []byte(first))
	if !strings.HasPrefix(a, "magpie-") || len(a) <= len("magpie-") {
		t.Fatalf("derived id %q lacks magpie- prefix", a)
	}
	if b := OpenCodeSession(nil, []byte(later)); b != a {
		t.Errorf("later turn id %q != first turn id %q", b, a)
	}
	if c := OpenCodeSession(nil, []byte(other)); c == a {
		t.Errorf("different conversation shares id %q", c)
	}
}

func TestOpenCodeSessionResponsesInput(t *testing.T) {
	asString := []byte(`{"input":"write a parser"}`)
	asArray := []byte(`{"input":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"input_text","text":"write a parser"}]}]}`)
	chat := []byte(`{"messages":[{"role":"user","content":"write a parser"}]}`)
	want := OpenCodeSession(nil, chat)
	if got := OpenCodeSession(nil, asString); got != want {
		t.Errorf("string input id %q != chat id %q", got, want)
	}
	if got := OpenCodeSession(nil, asArray); got != want {
		t.Errorf("array input id %q != chat id %q", got, want)
	}
}
