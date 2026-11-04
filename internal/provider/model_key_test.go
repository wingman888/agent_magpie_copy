package provider

import "testing"

func TestNormalizeModelKeySameModel(t *testing.T) {
	same := []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"anthropic/Claude-Opus-5.5-20260801",
	}
	want := NormalizeModelKey(same[0])
	for _, id := range same {
		if got := NormalizeModelKey(id); got != want {
			t.Errorf("NormalizeModelKey(%q) = %q, want %q", id, got, want)
		}
	}
	if want != "claude-opus-5-5" {
		t.Errorf("NormalizeModelKey(%q) = %q", same[0], want)
	}
}

func TestNormalizeModelKeyVariantsAndDots(t *testing.T) {
	if NormalizeModelKey("x:batch") == NormalizeModelKey("x") {
		t.Error("a colon variant must stay a model of its own")
	}
	if got := NormalizeModelKey("x:batch"); got != "x:batch" {
		t.Errorf("colon variant rewritten: %q", got)
	}
	if got := NormalizeModelKey("anthropic/x.5:batch"); got != "x.5:batch" {
		t.Errorf("colon variant kept as is: %q", got)
	}
	if got := NormalizeModelKey("v1.beta"); got != "v1.beta" {
		t.Errorf("non-version dot rewritten: %q", got)
	}
	if got := NormalizeModelKey("gpt-4.1-mini"); got != "gpt-4-1-mini" {
		t.Errorf("version dot: %q", got)
	}
}
