package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// turnStep is one request of a conversation as the gateway handles it: the
// candidates planned for its turn, affinity applied as gateway.go applies
// it, and the key that answers (recorded, with a warm cache, as the reply).
// It returns the key the request went to and why affinity said so.
func f09TurnStep(t *testing.T, s *Server, p provider.Provider, session string, turn int, within bool) (string, string) {
	t.Helper()
	now := time.Now()
	tn := turnRef{session: session, model: "m", turn: turn, within: within, rotating: p.Routing == provider.Rotate}
	cands, _ := s.planIn(p, "m", provider.Chat, tn)
	fromAt, to, at := affine(cands, session, "m", p.Affinity, tn, now)
	if fromAt >= 0 {
		cands = shift(cands, fromAt, to)
	}
	if at == nil {
		t.Fatal("no affinity trace for a session with several keys")
	}
	recordReply(session, "m", cands[0], 2000) // the vendor read its cache for this answer
	return cands[0].rest, at.Why
}

// A rotating key pool under the automatic affinity: a new user turn takes the
// next key even when the last answer was cached, while the requests within a
// turn (tool results sent back) stay on the key the turn started on.
func TestF09RotateAutoAffinityMovesOnEachTurn(t *testing.T) {
	f09KeyPoolSetup(t)
	p := provider.Provider{ID: "rotaff", Name: "RotAff", Routing: provider.Rotate, Chat: "http://relay.test/v1",
		Models: []string{"m"}, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	rest := func(k string) string { return "rotaff#" + provider.KeyID(k) }
	s := New()
	const ses = "ses-rot-auto"
	for _, c := range []struct {
		turn   int
		within bool
		want   string
		why    string
	}{
		{1, false, rest("k1"), "first"},
		{1, true, rest("k1"), "turn"}, // tool result
		{2, false, rest("k2"), "new-turn"},
		{2, true, rest("k2"), "turn"}, // tool result
		{3, false, rest("k3"), "new-turn"},
		{4, false, rest("k1"), "new-turn"},
	} {
		got, why := f09TurnStep(t, s, p, ses, c.turn, c.within)
		if got != c.want || why != c.why {
			t.Fatalf("turn %d within=%v: %s (%s), want %s (%s)", c.turn, c.within, got, why, c.want, c.why)
		}
	}
}

// The session mode keeps the whole conversation to one key, rotation or not:
// an explicit mode outranks the rotating pool.
func TestF09RotateSessionAffinityStillKeeps(t *testing.T) {
	f09KeyPoolSetup(t)
	p := provider.Provider{ID: "rotses", Name: "RotSes", Routing: provider.Rotate, Affinity: provider.AffinitySession,
		Chat: "http://relay.test/v1", Models: []string{"m"}, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}}}
	k1 := "rotses#" + provider.KeyID("k1")
	s := New()
	const ses = "ses-rot-session"
	for turn := 1; turn <= 3; turn++ {
		got, why := f09TurnStep(t, s, p, ses, turn, false)
		if turn == 1 {
			if got != k1 || why != "first" {
				t.Fatalf("turn 1: %s (%s), want %s (first)", got, why, k1)
			}
			continue
		}
		if got != k1 || why != "session" {
			t.Fatalf("turn %d: %s (%s), want %s (session)", turn, got, why, k1)
		}
	}
}

// Without rotation, the automatic affinity still keeps across turns while
// the cache is warm: the rotate rule is limited to rotating pools.
func TestF09SmartAutoAffinityStillKeepsAcrossTurns(t *testing.T) {
	f09KeyPoolSetup(t)
	p := provider.Provider{ID: "smartaff", Name: "SmartAff", Chat: "http://relay.test/v1",
		Models: []string{"m"}, Key: "k1", Keys: []provider.KeyAccount{{Key: "k2"}}}
	s := New()
	const ses = "ses-smart-auto"
	first, why := f09TurnStep(t, s, p, ses, 1, false)
	if why != "first" {
		t.Fatalf("turn 1: %s, want first", why)
	}
	got, why := f09TurnStep(t, s, p, ses, 2, false)
	if why != "cache" || got != first {
		t.Fatalf("turn 2: %s (%s), want %s (cache)", got, why, first)
	}
}
