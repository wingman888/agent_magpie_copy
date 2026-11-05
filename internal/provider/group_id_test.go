package provider

import "testing"

// An auto group's id is "auto-" and its canonical name, with nothing an id
// may not hold: no fingerprint is added.
func TestAutoIDIsPlainCanonicalName(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5": "auto-claude-opus-5-5",
		"v1.beta":         "auto-v1-beta",
		"x:batch":         "auto-x-batch",
	} {
		if got := autoID(in); got != want {
			t.Errorf("autoID(%q) = %q, want %q", in, got, want)
		}
	}
}
