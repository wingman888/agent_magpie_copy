package provider

import "testing"

func TestNormalizeModelKey(t *testing.T) {
	same := []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"Anthropic/Claude-Opus-5.5-20260801",
	}
	for _, s := range same {
		if got := NormalizeModelKey(s); got != "claude-opus-5-5" {
			t.Errorf("NormalizeModelKey(%q) = %q, want claude-opus-5-5", s, got)
		}
	}
	if a, b := NormalizeModelKey("x:batch"), NormalizeModelKey("x"); a == b {
		t.Errorf("x:batch and x should differ, both %q", a)
	}
	if got := NormalizeModelKey("x:batch"); got != "x:batch" {
		t.Errorf("x:batch = %q, want it kept", got)
	}
	if got := NormalizeModelKey("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta = %q, want it kept", got)
	}
	if got := NormalizeModelKey("gpt-4.1.2"); got != "gpt-4-1-2" {
		t.Errorf("gpt-4.1.2 = %q, want gpt-4-1-2", got)
	}
	if got := NormalizeModelKey("claude-opus-5-5:batch"); got != "claude-opus-5-5:batch" {
		t.Errorf("colon variant = %q", got)
	}
	// the vendor prefix is everything up to the last slash, as modelFamily reads it
	if got := NormalizeModelKey("openrouter/anthropic/claude-opus-5.5"); got != "claude-opus-5-5" {
		t.Errorf("nested prefix = %q, want claude-opus-5-5", got)
	}
}
