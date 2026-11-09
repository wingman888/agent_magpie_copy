package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestKfoRestingKeyComesAfterFallback: a resting key of the primary is tried
// after every ready candidate, the fallback's included, not just after the
// primary's own ready keys.
func TestKfoRestingKeyComesAfterFallback(t *testing.T) {
	kfoSetup(t, kfoPlan(provider.ExtraKey{Name: "团队", Key: kfoTeam}), &kfoUpstream{}, &kfoUpstream{})
	plan, err := provider.Find("kfoplan")
	if err != nil {
		t.Fatal(err)
	}
	s := New()
	s.rest(restKey(provider.Provider{ID: "kfoplan", Key: kfoFront}))

	var got []string
	for _, c := range s.candidates(*plan, "m1") {
		got = append(got, c.p.Key)
	}
	if want := strings.Join([]string{kfoTeam, kfoSpare, kfoFront}, ","); strings.Join(got, ",") != want {
		t.Fatalf("attempt order %s, want %s", strings.Join(got, ","), want)
	}
}
