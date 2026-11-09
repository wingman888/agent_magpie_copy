package provider

import "testing"

// Spellings of one model from different vendors meet in one key.
func TestS1NormalizeModelKeyExamples(t *testing.T) {
	same := []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801"}
	for _, m := range same {
		if got := NormalizeModelKey(m); got != "claude-opus-5-5" {
			t.Errorf("NormalizeModelKey(%q) = %q, want claude-opus-5-5", m, got)
		}
	}
	if NormalizeModelKey("x:batch") == NormalizeModelKey("x") {
		t.Errorf("a colon variant must stay a different model: %q vs %q", NormalizeModelKey("x:batch"), NormalizeModelKey("x"))
	}
	if got := NormalizeModelKey("x:batch"); got != "x:batch" {
		t.Errorf("x:batch = %q", got)
	}
	if got := NormalizeModelKey("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta = %q, want unchanged", got)
	}
}

// A date only ends a model name when it is eight digits after a hyphen, and
// a dot only reads as a version point between two digits.
func TestS1NormalizeModelKeyEdges(t *testing.T) {
	for in, want := range map[string]string{
		"gpt-4.1-mini":                  "gpt-4-1-mini",
		"openrouter/openai/o3-mini":     "o3-mini",
		"claude-3-20240229":             "claude-3",
		"model-2026080":                 "model-2026080",
		"  Anthropic/Claude-Opus-5.5  ": "claude-opus-5-5",
		"x.y.5.5":                       "x.y.5-5",
		"qwen2.5:7b":                    "qwen2-5:7b",
	} {
		if got := NormalizeModelKey(in); got != want {
			t.Errorf("NormalizeModelKey(%q) = %q, want %q", in, got, want)
		}
	}
}
