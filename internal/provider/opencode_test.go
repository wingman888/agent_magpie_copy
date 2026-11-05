package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	cases := map[string]PresetDef{
		"opencode-go": {Chat: "https://opencode.ai/zen/go/v1", Responses: "https://opencode.ai/zen/go/v1",
			Anthropic: "https://opencode.ai/zen/go", Website: "https://opencode.ai/docs/go", KeysURL: "https://opencode.ai/auth"},
		"opencode-zen": {Chat: "https://opencode.ai/zen/v1", Responses: "https://opencode.ai/zen/v1",
			Anthropic: "https://opencode.ai/zen", Website: "https://opencode.ai/docs/zen", KeysURL: "https://opencode.ai/auth"},
	}
	for id, want := range cases {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %s missing", id)
		}
		if pr.Chat != want.Chat || pr.Responses != want.Responses || pr.Anthropic != want.Anthropic ||
			pr.Website != want.Website || pr.KeysURL != want.KeysURL {
			t.Errorf("%s: got %+v", id, *pr)
		}
		p, err := FromPreset(id)
		if err != nil || !p.IsOpenCode() {
			t.Errorf("%s: FromPreset %v, IsOpenCode %v", id, err, p.IsOpenCode())
		}
	}
}

func TestIsOpenCode(t *testing.T) {
	for host, want := range map[string]bool{
		"opencode.ai":          true,
		"api.opencode.ai":      true,
		"zen.opencode.ai:443":  true,
		"notopencode.ai":       false,
		"opencode.ai.evil.com": false,
		"example.com":          false,
	} {
		p := Provider{Chat: "https://" + host + "/v1"}
		if got := p.IsOpenCode(); got != want {
			t.Errorf("%s: IsOpenCode = %v, want %v", host, got, want)
		}
		if got := IsOpenCodeHost(host); got != want {
			t.Errorf("IsOpenCodeHost(%s) = %v, want %v", host, got, want)
		}
	}
}

func TestOpenCodeSessionHeaderPriority(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	h := http.Header{}
	h.Set("session-id", "from-session-id")
	h.Set("x-claude-code-session-id", "from-claude")
	if got := OpenCodeSession(h, body); got != "from-session-id" {
		t.Errorf("got %q", got)
	}
	h.Set("x-opencode-session", "oc-1")
	if got := OpenCodeSession(h, body); got != "oc-1" {
		t.Errorf("got %q", got)
	}
	h = http.Header{}
	h.Set("x-session-affinity", "  ")
	h.Set("session_id", "sid")
	if got := OpenCodeSession(h, body); got != "sid" {
		t.Errorf("blank values must be skipped: got %q", got)
	}
}

func TestOpenCodeSessionFallbackIsStablePerConversation(t *testing.T) {
	first := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"write a parser"}]}`
	later := `{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"write a parser"},` +
		`{"role":"assistant","content":"ok"},{"role":"user","content":"more"}]}`
	other := `{"messages":[{"role":"user","content":"something else"}]}`
	a := OpenCodeSession(nil, []byte(first))
	if !strings.HasPrefix(a, "magpie-") {
		t.Fatalf("got %q", a)
	}
	if b := OpenCodeSession(nil, []byte(later)); b != a {
		t.Errorf("later turn changed id: %q vs %q", b, a)
	}
	if c := OpenCodeSession(nil, []byte(other)); c == a {
		t.Errorf("different conversations share %q", a)
	}
}

func TestOpenCodeSessionFallbackResponsesAndAnthropic(t *testing.T) {
	chat := OpenCodeSession(nil, []byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	respString := OpenCodeSession(nil, []byte(`{"input":"hello"}`))
	respArray := OpenCodeSession(nil, []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`))
	respLater := OpenCodeSession(nil, []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]},`+
		`{"type":"function_call_output","call_id":"c1","output":"x"}]}`))
	anth := OpenCodeSession(nil, []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	for name, got := range map[string]string{"responses string": respString, "responses array": respArray,
		"responses later": respLater, "anthropic": anth} {
		if got != chat {
			t.Errorf("%s: %q, want %q", name, got, chat)
		}
	}
}

func TestOpenCodeSessionFallbackImageOnlyFirstTurn(t *testing.T) {
	img := func(data string) []byte {
		return []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + data + `"}}]}]}`)
	}
	a := OpenCodeSession(nil, img("AAAA"))
	b := OpenCodeSession(nil, img("BBBB"))
	if !strings.HasPrefix(a, "magpie-") || !strings.HasPrefix(b, "magpie-") {
		t.Fatalf("got %q, %q", a, b)
	}
	if a == b {
		t.Errorf("different image-only conversations share %q", a)
	}
	if again := OpenCodeSession(nil, img("AAAA")); again != a {
		t.Errorf("same image-only conversation changed id: %q vs %q", again, a)
	}
	if e1, e2 := OpenCodeSession(nil, []byte(`{"input":""}`)), OpenCodeSession(nil, []byte(`{"input":"x"}`)); e1 == e2 {
		t.Errorf("empty input collides with non-empty input")
	}
}
