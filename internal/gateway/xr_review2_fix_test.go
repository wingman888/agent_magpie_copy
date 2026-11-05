package gateway

import (
	"net/http"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Bodies of one conversation as an agent sends it: a user turn, the same turn
// with its tool result back, the next user turn, and a request of another
// model in the same session.
const (
	xrRqTurn1 = `{"model":"m1","messages":[{"role":"user","content":"hi"}]}`
	xrRqTool1 = `{"model":"m1","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"out"}]}`
	xrRqTurn2 = `{"model":"m1","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"again"}]}`
	xrRqTurn3 = `{"model":"m1","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"},{"role":"user","content":"again"},{"role":"assistant","content":"ok"},{"role":"user","content":"third"}]}`
	xrRqTitle = `{"model":"m2","messages":[{"role":"user","content":"title this"}]}`
)

// Another model's requests in the same session do not move the rotation of
// the model the conversation is on (I2).
func TestXRRotationIgnoresOtherModelsInSession(t *testing.T) {
	xrGrpResetR2(t)
	s := &Server{}
	p := provider.Provider{ID: "rq", Name: "RQ", Chat: "http://127.0.0.1:1/v1", Routing: provider.Rotate, Key: "k1",
		Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	h := http.Header{}
	h.Set("x-session-id", "sess-rq")
	call := func(body string) string {
		gc := groupCallOf(h, provider.Chat, []byte(body), "")
		model := modelOf([]byte(body))
		cs, _ := s.planFor(p, model, provider.Chat, gc)
		return cs[0].p.Key
	}
	var m1 []string
	for _, body := range []string{xrRqTurn1, xrRqTitle, xrRqTool1, xrRqTitle, xrRqTurn2, xrRqTitle, xrRqTurn3, xrRqTurn1} {
		if k := call(body); modelOf([]byte(body)) == "m1" {
			m1 = append(m1, k)
		}
	}
	want := []string{"k1", "k1", "k2", "k3", "k1"}
	if !xrEqualR2(m1, want) {
		t.Fatalf("m1 keys by turn: %v, want %v", m1, want)
	}
}

// A routing group in order keeps a rotating member's rotation by turn, not by
// request: a turn's tool results stay on its key (I3).
func TestXROrderedGroupRotatesMemberByTurn(t *testing.T) {
	xrGrpResetR2(t)
	s := &Server{}
	p := provider.Provider{ID: "og", Name: "OG", Chat: "http://127.0.0.1:1/v1", Routing: provider.Rotate, Key: "k1",
		Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	ms := []provider.Member{{Provider: p, Model: "gpt-5.5"}}
	g := provider.Group{ID: "x", Routing: provider.Ordered}
	h := http.Header{}
	h.Set("x-session-id", "sess-og")
	call := func(body string) string {
		gc := groupCallOf(h, provider.Chat, []byte(body), "x")
		cs, _, _ := s.planGroup(g, ms, provider.Chat, gc)
		return cs[0].p.Key
	}
	got := []string{call(xrRqTurn1), call(xrRqTool1), call(xrRqTurn2), call(xrRqTool1), call(xrRqTurn3)}
	want := []string{"k1", "k1", "k2", "k2", "k3"}
	if !xrEqualR2(got, want) {
		t.Fatalf("keys by turn: %v, want %v", got, want)
	}
}

// The trace's affinity mode is the group's own setting, not the member
// provider's, which the decision inherits (I4).
func TestXRGroupTraceModeIsGroupsOwn(t *testing.T) {
	xrGrpResetR2(t)
	s := &Server{}
	pa := provider.Provider{ID: "ta", Name: "TA", Chat: "http://127.0.0.1:1/v1", Key: "k1",
		Affinity: provider.AffinitySession, Keys: []provider.KeyAccount{{Key: "k2"}}}
	ms := []provider.Member{{Provider: pa, Model: "gpt-5.5"}}
	gc := &groupCall{group: "x", session: "sess-i4"}
	first, _, _ := s.planGroup(provider.Group{ID: "x"}, ms, provider.Chat, gc)
	gc.answered(first[1], 0)
	_, _, aff := s.planGroup(provider.Group{ID: "x"}, ms, provider.Chat, &groupCall{group: "x", session: "sess-i4", turns: 2})
	if aff == nil || aff.Why != "session" || !aff.Kept {
		t.Fatalf("affinity %+v, want kept by session", aff)
	}
	if aff.Mode != "" {
		t.Fatalf("mode %q, want the group's own empty affinity, not the member's %q", aff.Mode, provider.AffinitySession)
	}
}

// A rotating group does not keep a new turn to the key that answered the last
// one by the cache; within a turn it does (I6).
func TestXRRotatingGroupCacheDoesNotHoldAcrossTurns(t *testing.T) {
	xrGrpResetR2(t)
	s := &Server{}
	pa := provider.Provider{ID: "tr", Name: "TR", Chat: "http://127.0.0.1:1/v1", Key: "k1",
		Keys: []provider.KeyAccount{{Key: "k2"}}}
	ms := []provider.Member{{Provider: pa, Model: "gpt-5.5"}}
	g := provider.Group{ID: "x", Routing: provider.Rotate}
	gc := &groupCall{group: "x", session: "sess-i6"}
	first, _, _ := s.planGroup(g, ms, provider.Chat, gc)
	gc.answered(first[0], 2000)

	cs, _, aff := s.planGroup(g, ms, provider.Chat, &groupCall{group: "x", session: "sess-i6", turns: 2})
	if aff == nil || aff.Why != "new-turn" || aff.Kept {
		t.Fatalf("new turn: affinity %+v, want new-turn and not kept", aff)
	}
	if cs[0].rest == first[0].rest {
		t.Fatalf("new turn stayed on %s; the rotation should have moved on", cs[0].rest)
	}

	cs, _, aff = s.planGroup(g, ms, provider.Chat, &groupCall{group: "x", session: "sess-i6", turns: 2, within: true})
	if aff == nil || aff.Why != "turn" || !aff.Kept || cs[0].rest != first[0].rest {
		t.Fatalf("within the turn: affinity %+v, first %s, want kept on %s", aff, cs[0].rest, first[0].rest)
	}
}

// A direct request with a session keeps to the key that answered it, by its
// provider's affinity (I1); without a session it has no affinity at all.
func TestXRDirectRequestKeepsKeyByAffinity(t *testing.T) {
	xrGrpResetR2(t)
	s := &Server{}
	p := provider.Provider{ID: "da", Name: "DA", Chat: "http://127.0.0.1:1/v1", Key: "k1",
		Affinity: provider.AffinitySession, Keys: []provider.KeyAccount{{Key: "k2"}}}
	h := http.Header{}
	h.Set("x-session-id", "sess-da")
	gc := groupCallOf(h, provider.Chat, []byte(xrRqTurn1), "")
	cs, pl := s.planFor(p, "gpt-5.5", provider.Chat, gc)
	if pl.aff == nil || pl.aff.Why != "first" || pl.aff.Mode != provider.AffinitySession {
		t.Fatalf("first request: affinity %+v, want first", pl.aff)
	}
	keyedCall(gc, p, "gpt-5.5").answered(cs[1], 0)

	gc2 := groupCallOf(h, provider.Chat, []byte(xrRqTurn2), "")
	cs2, pl2 := s.planFor(p, "gpt-5.5", provider.Chat, gc2)
	if pl2.aff == nil || pl2.aff.Why != "session" || !pl2.aff.Kept || cs2[0].rest != cs[1].rest {
		t.Fatalf("second request: affinity %+v, first %s, want kept on %s", pl2.aff, cs2[0].rest, cs[1].rest)
	}

	// another model of the provider has its own conversation memory
	cs3, pl3 := s.planFor(p, "other", provider.Chat, groupCallOf(h, provider.Chat, []byte(xrRqTurn2), ""))
	if pl3.aff == nil || pl3.aff.Why != "first" || pl3.aff.Kept || cs3[0].rest != cs[0].rest {
		t.Fatalf("other model: affinity %+v, first %s", pl3.aff, cs3[0].rest)
	}

	_, plNo := s.planFor(p, "gpt-5.5", provider.Chat, nil)
	if plNo.aff != nil {
		t.Fatalf("no session: affinity %+v, want none", plNo.aff)
	}
}

func xrEqualR2(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func xrGrpResetR2(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.note = map[string]Rest{}
	restingUntil.Unlock()
	groupMemory.Lock()
	groupMemory.answers = map[string]groupAnswer{}
	groupMemory.turns = map[string]groupTurn{}
	groupMemory.Unlock()
	t.Cleanup(func() { clock = time.Now })
}
