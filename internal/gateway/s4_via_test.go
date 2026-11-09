package gateway

import "testing"

func TestS4ViaGroup(t *testing.T) {
	for in, want := range map[string]string{
		"group/mine":      "group/mine",
		"GROUP/Mine":      "group/Mine",
		"claude-opus-5-5": "",
		"a/m":             "",
	} {
		if got := viaGroup(in); got != want {
			t.Errorf("viaGroup(%q) = %q, want %q", in, got, want)
		}
	}
}
