package agent

import "testing"

// FIX: a routing group reference is a magpie model, as the provider models are.
func TestFIXIsMagpieGroupRef(t *testing.T) {
	for v, want := range map[string]bool{"group/mine": true, "group/": false} {
		if got := isMagpie(v); got != want {
			t.Errorf("isMagpie(%q) = %v, want %v", v, got, want)
		}
	}
}
