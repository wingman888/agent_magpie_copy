package provider

import (
	"net/http"
	"strings"
	"testing"
)

func TestIsOpenCode(t *testing.T) {
	for base, want := range map[string]bool{
		"https://opencode.ai/zen/v1": true, "https://api.opencode.ai/v1": true,
		"https://notopencode.ai/v1": false, "https://opencode.ai.evil.com/v1": false, "https://x.com/opencode.ai": false,
	} {
		if got := (Provider{Chat: base}).IsOpenCode(); got != want {
			t.Errorf("%s: %v", base, got)
		}
	}
}

func TestOpenCodePresets(t *testing.T) {
	want := map[string][4]string{
		"opencode-go":  {"https://opencode.ai/zen/go/v1", "https://opencode.ai/zen/go", "https://opencode.ai/docs/go", "https://opencode.ai/auth"},
		"opencode-zen": {"https://opencode.ai/zen/v1", "https://opencode.ai/zen", "https://opencode.ai/docs/zen", "https://opencode.ai/auth"},
	}
	n := 0
	for _, p := range Presets() {
		w, ok := want[p.ID]
		if !ok {
			continue
		}
		n++
		if p.Chat != w[0] || p.Responses != w[0] || p.Anthropic != w[1] || p.Website != w[2] || p.KeysURL != w[3] {
			t.Errorf("%s: %+v", p.ID, p)
		}
	}
	if n != 2 {
		t.Fatal("presets missing")
	}
}

func TestOpenCodeSessionValue(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	a, b := OpenCodeSession(http.Header{}, body), OpenCodeSession(http.Header{}, body)
	if !strings.HasPrefix(a, "magpie-") || a != b {
		t.Fatal(a)
	}
	h := http.Header{}
	h.Set("session-id", "z")
	h.Set("x-session-affinity", "  ")
	if OpenCodeSession(h, body) != "z" {
		t.Fatal("header")
	}
}
