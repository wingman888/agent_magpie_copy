package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// finalTierCands is a main-tier list of candidates of the given providers,
// in order, with the matching trace entries.
func finalTierCands(ids ...string) ([]candidate, []Weighed) {
	cs := make([]candidate, len(ids))
	ws := make([]Weighed, len(ids))
	for i, id := range ids {
		cs[i] = candidate{p: provider.Provider{ID: id}, model: "m", rest: id + "#k" + string(rune('0'+i))}
		ws[i] = Weighed{ID: cs[i].rest, Provider: id}
	}
	return cs, ws
}

func finalOrderOf(cs []candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.rest
	}
	return out
}

func TestFinalAffinityStaysWithinProvider(t *testing.T) {
	// the answerer is the fallback provider's second key; the primary's keys
	// stay ahead, and only the fallback's own keys reorder.
	cs, ws := finalTierCands("a", "a", "b", "b")
	cs, _ = moveToTierFront(cs, ws, 3, 4)
	got := finalOrderOf(cs)
	want := []string{"a#k0", "a#k1", "b#k3", "b#k2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fallback answerer moved past the primary: got %v, want %v", got, want)
		}
	}

	// the primary's own second key moves to its provider's front only
	cs, ws = finalTierCands("a", "b", "a")
	cs, _ = moveToTierFront(cs, ws, 2, 3)
	got = finalOrderOf(cs)
	want = []string{"a#k0", "b#k1", "a#k2"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("a key crossed another provider's key: got %v, want %v", got, want)
		}
	}
}
