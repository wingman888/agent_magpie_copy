package provider

import "testing"

// The same model, spelled the ways vendors spell it, comes out one name.
func TestCanonicalModelSameModel(t *testing.T) {
	same := []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"anthropic/claude-opus-5.5-20260801",
		"  CLAUDE-OPUS-5.5  ",
	}
	want := CanonicalModel(same[0])
	if want != "claude-opus-5-5" {
		t.Fatalf("canonical of %q = %q", same[0], want)
	}
	for _, s := range same[1:] {
		if got := CanonicalModel(s); got != want {
			t.Errorf("CanonicalModel(%q) = %q, want %q", s, got, want)
		}
	}
}

// A colon variant is another model, and kept as written in lower case.
func TestCanonicalModelVariants(t *testing.T) {
	if got := CanonicalModel("claude-opus-5-5:batch"); got != "claude-opus-5-5:batch" {
		t.Errorf("variant: %q", got)
	}
	if got := CanonicalModel("claude-opus-5-5:batch"); got == CanonicalModel("claude-opus-5-5") {
		t.Errorf("a variant must not be the base model")
	}
	if got := CanonicalModel("anthropic/claude-opus-5.5:BATCH"); got != "claude-opus-5-5:batch" {
		t.Errorf("prefixed variant: %q", got)
	}
}

// Dots that aren't between version numbers are left alone.
func TestCanonicalModelDots(t *testing.T) {
	if got := CanonicalModel("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta: %q", got)
	}
	if got := CanonicalModel("gpt-4.1-mini"); got != "gpt-4-1-mini" {
		t.Errorf("gpt-4.1-mini: %q", got)
	}
	if got := CanonicalModel("glm-4.5.1"); got != "glm-4-5-1" {
		t.Errorf("glm-4.5.1: %q", got)
	}
}
