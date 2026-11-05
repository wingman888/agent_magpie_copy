package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Rotation counts per model asked for: another model of the same provider,
// asked in the same session between a conversation's turns, moves nothing.
func TestRotateIgnoresOtherModelOfSameProvider(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "rotq", srv, provider.Rotate, "", "q1", "q2", "q3")

	var turns []string
	msgs := []string{userMsg}
	for turn := 0; turn < 3; turn++ {
		ask(t, s, "rotq/m", "rotq-s", strings.Join(msgs, ","))
		turns = append(turns, keyOf(t, up, 1))
		ask(t, s, "rotq/m", "rotq-s", strings.Join(append(msgs, toolTurn), ","))
		keyOf(t, up, 1)
		// the same provider, another model, a new user message of its own
		ask(t, s, "rotq/other", "rotq-s", userMsg)
		up.take()
		msgs = append(msgs, nextMsg)
	}
	if got := strings.Join(turns, " "); got != "q1 q2 q3" {
		t.Fatalf("turns went to %q, want q1 q2 q3", got)
	}
}

// A request without a session id is not a conversation: no affinity at all.
func TestAffinityNeedsASessionHeader(t *testing.T) {
	s := isolate(t)
	up := &keyed{}
	srv := httptest.NewServer(up)
	defer srv.Close()
	keyedProvider(t, "nosess", srv, "", "", "n1", "n2")

	ask(t, s, "nosess/m", "", userMsg)
	keyOf(t, up, 1)
	r := ask(t, s, "nosess/m", "", userMsg+","+toolTurn)
	keyOf(t, up, 1)
	if r.Affinity != nil {
		t.Fatalf("affinity without a session header: %+v", r.Affinity)
	}
}

// A resting main-pool key is tried before any key aside, ready or not.
func TestSettleKeepsAsideBehindRestingMain(t *testing.T) {
	restingUntil.Lock()
	restingUntil.m["settle-p#main"] = time.Now().Add(time.Hour)
	restingUntil.note["settle-p#main"] = Rest{Why: failRate, Status: 429, Until: time.Now().Add(time.Hour), By: "cooldown"}
	restingUntil.Unlock()
	t.Cleanup(func() {
		restingUntil.Lock()
		delete(restingUntil.m, "settle-p#main")
		delete(restingUntil.note, "settle-p#main")
		restingUntil.Unlock()
	})
	out := []candidate{
		{p: provider.Provider{ID: "settle-p"}, model: "m", rest: "settle-p#aside", aside: true},
		{p: provider.Provider{ID: "settle-p"}, model: "m", rest: "settle-p#main"},
	}
	pl := planned{order: make([]Weighed, len(out))}
	cs, pl2 := settle(out, pl, "", convo{})
	if len(cs) != 2 || cs[0].rest != "settle-p#main" || cs[1].rest != "settle-p#aside" {
		t.Fatalf("order %q, want the resting main key before the aside one", []string{cs[0].rest, cs[1].rest})
	}
	if len(pl2.order) != 2 || pl2.order[0].Rest == nil || pl2.order[1].Rest != nil {
		t.Fatalf("trace order %+v: the resting main key should carry its rest, the aside one not", pl2.order)
	}
}

// Affinity matches the candidate that answered by its model too: the same
// key or account asked for another model is not the one that answered.
func TestAffinityMatchesCandidateModel(t *testing.T) {
	cv := convo{id: "affmodel-s", model: "agent/x", turns: 1}
	remember(cv, candidate{p: provider.Provider{ID: "affmodel"}, model: "m1", rest: "affmodel#k1"}, 4096)
	cs := []candidate{
		{p: provider.Provider{ID: "affmodel"}, model: "m2", rest: "affmodel#k1"},
		{p: provider.Provider{ID: "affmodel"}, model: "m1", rest: "affmodel#k1"},
	}
	k, aff := keepLast("", cv, cs)
	if k != 1 || !aff.Kept || aff.Why != "cache" {
		t.Fatalf("kept index %d, affinity %+v; want index 1 kept by cache", k, aff)
	}
	other := []candidate{{p: provider.Provider{ID: "affmodel"}, model: "m2", rest: "affmodel#k1"}}
	if k, aff := keepLast("", cv, other); k != -1 || aff.Why != "gone" {
		t.Fatalf("same key, other model only: index %d, affinity %+v; want gone", k, aff)
	}
}
