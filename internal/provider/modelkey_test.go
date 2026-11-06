package provider

import "testing"

func TestNormalizeModelKey_f09(t *testing.T) {
	same := []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"anthropic/Claude-Opus-5.5-20260801",
	}
	for _, m := range same {
		if got := normalizeModelKey(m); got != "claude-opus-5-5" {
			t.Errorf("normalizeModelKey(%q) = %q, want claude-opus-5-5", m, got)
		}
	}
	if a, b := normalizeModelKey("x:batch"), normalizeModelKey("x"); a == b {
		t.Errorf("x:batch and x both %q; a colon variant is another model", a)
	}
	if got := normalizeModelKey("x:batch"); got != "x:batch" {
		t.Errorf("x:batch = %q", got)
	}
	if got := normalizeModelKey("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta = %q, want unchanged", got)
	}
	if got := normalizeModelKey("gpt-5.5.1"); got != "gpt-5-5-1" {
		t.Errorf("gpt-5.5.1 = %q", got)
	}
	if got := normalizeModelKey("Anthropic/Claude-Opus-5.5:BATCH"); got != "claude-opus-5-5:batch" {
		t.Errorf("variant = %q", got)
	}
}
