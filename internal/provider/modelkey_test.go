package provider

import "testing"

// The same model under the names vendors give it is one key.
func TestCanonicalModelKeySameModel(t *testing.T) {
	want := "claude-opus-5-5"
	for _, name := range []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"anthropic/claude-opus-5.5-20260801",
	} {
		if got := CanonicalModelKey(name); got != want {
			t.Errorf("CanonicalModelKey(%q) = %q, want %q", name, got, want)
		}
	}
}

// A variant after a colon is another model, and a dot that is not between
// two digits is left as it is.
func TestCanonicalModelKeyKeeps(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"claude-opus-5-5:batch", "claude-opus-5-5:batch"},
		{"claude-opus-5.5:batch", "claude-opus-5-5:batch"},
		{"v1.beta", "v1.beta"},
		{"V1.Beta", "v1.beta"},
		{"gpt-4.1-mini", "gpt-4-1-mini"},
		{"claude-3.5-sonnet-20241022", "claude-3-5-sonnet"},
		{"a/b/c.1.2", "b/c.1-2"},
	} {
		if got := CanonicalModelKey(c.in); got != c.want {
			t.Errorf("CanonicalModelKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
