package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestOpenCodePresets(t *testing.T) {
	want := map[string][3]string{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen/v1", "https://opencode.ai/zen"},
	}
	for id, ends := range want {
		pr := Preset(id)
		if pr == nil {
			t.Fatalf("preset %q missing", id)
		}
		if pr.Chat != ends[0] || pr.Responses != ends[1] || pr.Anthropic != ends[2] {
			t.Errorf("%s endpoints = %q %q %q, want %q", id, pr.Chat, pr.Responses, pr.Anthropic, ends)
		}
		if pr.KeysURL != "https://opencode.ai/auth" {
			t.Errorf("%s keys url = %q", id, pr.KeysURL)
		}
	}
	if pr := Preset("opencode-go"); pr.Website != "https://opencode.ai/docs/go" {
		t.Errorf("go website = %q", pr.Website)
	}
	if pr := Preset("opencode-zen"); pr.Website != "https://opencode.ai/docs/zen" {
		t.Errorf("zen website = %q", pr.Website)
	}
	listed := map[string]bool{}
	for _, pr := range Presets() {
		listed[pr.ID] = true
	}
	if !listed["opencode-go"] || !listed["opencode-zen"] {
		t.Errorf("presets list lacks OpenCode entries: %v", listed)
	}
}

func TestIsOpenCode(t *testing.T) {
	cases := map[string]bool{
		"https://opencode.ai/zen/go/v1":  true,
		"https://api.opencode.ai/v1":     true,
		"https://OPENCODE.AI/zen":        true,
		"https://notopencode.ai/v1":      false,
		"https://opencode.ai.evil.com/v": false,
		"https://api.deepseek.com/v1":    false,
	}
	for base, want := range cases {
		p := Provider{ID: "x", Chat: base}
		if got := p.IsOpenCode(); got != want {
			t.Errorf("IsOpenCode(%q) = %v, want %v", base, got, want)
		}
	}
	if !(Provider{ID: "x", Anthropic: "https://opencode.ai/zen"}).IsOpenCode() {
		t.Error("anthropic-only OpenCode provider not recognized")
	}
	if !(Provider{ID: "g", Chat: "https://opencode.ai/zen/go/v1"}).IsOpenCode() {
		t.Error("go provider not recognized")
	}
}

func TestIsOpenCodeHostPort(t *testing.T) {
	if !IsOpenCodeHost("opencode.ai:443") {
		t.Error("port should be ignored")
	}
	if IsOpenCodeHost("notopencode.ai") || IsOpenCodeHost("") {
		t.Error("wrong host accepted")
	}
}

func TestOpenCodeSessionPrefersAgentHeader(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	cases := []struct {
		name, header, value string
	}{
		{"opencode", "x-opencode-session", "oc-1"},
		{"affinity", "x-session-affinity", "aff-1"},
		{"session-id", "x-session-id", "sid-1"},
		{"underscore", "session_id", "us-1"},
		{"dash", "session-id", "dash-1"},
		{"claude-code", "x-claude-code-session-id", "cc-1"},
	}
	for _, c := range cases {
		in := http.Header{}
		in.Set(c.header, c.value)
		if got := OpenCodeSession(in, body); got != c.value {
			t.Errorf("%s: got %q, want %q", c.name, got, c.value)
		}
	}
	in := http.Header{}
	in.Set("x-session-id", "second")
	in.Set("x-opencode-session", "first")
	if got := OpenCodeSession(in, body); got != "first" {
		t.Errorf("priority: got %q, want first", got)
	}
	in = http.Header{}
	in.Set("x-opencode-session", "   ")
	in.Set("x-session-id", "next")
	if got := OpenCodeSession(in, body); got != "next" {
		t.Errorf("blank header should be skipped: got %q", got)
	}
}

func TestOpenCodeSessionDerivedIsStable(t *testing.T) {
	first := `{"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"write a parser"}]}`
	turn2 := `{"messages":[{"role":"system","content":"be nice"},{"role":"user","content":"write a parser"},{"role":"assistant","content":"sure"},{"role":"user","content":"now tests"}]}`
	other := `{"messages":[{"role":"user","content":"something else"}]}`

	a := OpenCodeSession(nil, []byte(first))
	b := OpenCodeSession(nil, []byte(turn2))
	c := OpenCodeSession(nil, []byte(other))
	if !strings.HasPrefix(a, "magpie-") {
		t.Fatalf("derived id %q lacks magpie- prefix", a)
	}
	if a != b {
		t.Errorf("later turn changed id: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("different conversations share id %q", a)
	}
}

func TestOpenCodeSessionResponsesInput(t *testing.T) {
	str := `{"input":"write a parser"}`
	arr := `{"input":[{"role":"user","content":[{"type":"input_text","text":"write a parser"}]}]}`
	turn2 := `{"input":[{"role":"user","content":[{"type":"input_text","text":"write a parser"}]},{"type":"function_call_output","call_id":"c1","output":"ok"},{"role":"user","content":"more"}]}`
	other := `{"input":[{"role":"user","content":"other conversation"}]}`

	a := OpenCodeSession(nil, []byte(str))
	if !strings.HasPrefix(a, "magpie-") {
		t.Fatalf("string input id %q", a)
	}
	if got := OpenCodeSession(nil, []byte(arr)); got != a {
		t.Errorf("array input id %q != string input id %q", got, a)
	}
	if got := OpenCodeSession(nil, []byte(turn2)); got != a {
		t.Errorf("later responses turn id %q != %q", got, a)
	}
	if got := OpenCodeSession(nil, []byte(other)); got == a {
		t.Errorf("different responses conversations share id %q", got)
	}
}

func TestOpenCodeSessionEmptyBodyStillPrefixed(t *testing.T) {
	if got := OpenCodeSession(nil, []byte(`{}`)); !strings.HasPrefix(got, "magpie-") {
		t.Errorf("got %q", got)
	}
}
