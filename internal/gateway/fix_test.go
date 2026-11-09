package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// TestFixRestingBeforeCache: a last answerer that is resting is not kept
// under automatic affinity across turns, even with no cache to keep it for.
func TestFixRestingBeforeCache(t *testing.T) {
	fixK1 := candidate{p: provider.Provider{ID: "fixp", Key: "fix-k1"}, model: "fm", rest: "fixp#fix-k1"}
	fixK2 := candidate{p: provider.Provider{ID: "fixp", Key: "fix-k2"}, model: "fm", rest: "fixp#fix-k2"}
	const scope = "fixp/fm"
	list := func() ([]candidate, []Weighed) {
		return []candidate{fixK1, fixK2}, []Weighed{{ID: fixK1.rest}, {ID: fixK2.rest}}
	}
	// the answerer rests for an hour
	rest := func(on bool) {
		restingUntil.Lock()
		defer restingUntil.Unlock()
		if on {
			restingUntil.m[fixK2.rest] = time.Now().Add(time.Hour)
			restingUntil.note[fixK2.rest] = Rest{}
		} else {
			delete(restingUntil.m, fixK2.rest)
			delete(restingUntil.note, fixK2.rest)
		}
	}
	t.Cleanup(func() { rest(false) })

	cases := []struct {
		name, conv, mode string
		resting          bool
		cacheRead        int
		age              time.Duration
		tc               turnCtx
		why              string
		kept             bool
	}{
		// auto, across turns, no cache read: normally "no-cache"
		{name: "control", conv: "fix-ctl", mode: "", resting: false, cacheRead: 0, age: time.Minute, why: "no-cache"},
		// same, but the answerer is resting: resting wins
		{name: "resting-auto-cross-turn", conv: "fix-rest", mode: "", resting: true, cacheRead: 0, age: time.Minute, why: "resting"},
		// off still says off, even when resting
		{name: "resting-off", conv: "fix-off", mode: provider.AffinityOff, resting: true, cacheRead: 2000, why: "off"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			rest(c.resting)
			defer rest(false)
			rememberSticky(c.conv, scope, fixK2, c.cacheRead)
			sticky.Lock()
			r := sticky.last[stickyKey(c.conv, scope)]
			r.at = time.Now().Add(-c.age)
			sticky.last[stickyKey(c.conv, scope)] = r
			sticky.Unlock()

			cs, ws := list()
			tc := turnCtx{conv: c.conv, turn: 2, scope: scope}
			out, _, aff := settle(cs, ws, c.mode, scope, "", tc)
			if aff == nil || aff.Why != c.why || aff.Kept != c.kept {
				t.Fatalf("affinity %+v, want why %s kept %v", aff, c.why, c.kept)
			}
			if aff.Why == "resting" && out[len(out)-1].rest != fixK2.rest {
				t.Fatalf("resting answerer not moved to the back: %s", out[len(out)-1].rest)
			}
		})
	}
}
