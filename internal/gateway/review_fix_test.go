package gateway

import (
	"net/http"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyNames lists a plan's candidates by key, with "aside" marked.
func keyNames(cs []candidate, pl planned) []string {
	var out []string
	for i, c := range cs {
		n := c.p.Key
		if i < len(pl.order) && pl.order[i].Aside {
			n += " aside"
		}
		out = append(out, n)
	}
	return out
}

// Keys are tiered across the whole group: a Chat key in one member puts a
// keyless-protocol key of another member aside, not in the pool with it.
func TestGroupTiersKeysAcrossMembers(t *testing.T) {
	grpReset(t)
	s := &Server{}
	pa := provider.Provider{ID: "ga", Name: "GA", Chat: "http://127.0.0.1:1/v1", Key: "ka", KeyProtocol: provider.Chat}
	pb := provider.Provider{ID: "gb", Name: "GB", Chat: "http://127.0.0.1:1/v1", Key: "kb"}
	ms := []provider.Member{{Provider: pb, Model: "gpt-5.5"}, {Provider: pa, Model: "gpt-5.5"}}
	cs, pl, _ := s.planGroup(provider.Group{ID: "x"}, ms, provider.Chat, &groupCall{group: "x"})
	if got, want := keyNames(cs, pl), []string{"ka", "kb aside"}; !equal(got, want) {
		t.Fatalf("smart: %v, want %v", got, want)
	}
}

// In order, a member with no key of the group's best tier has all its keys
// aside, after the members' pools.
func TestGroupOrderMemberWithoutBestTierIsAside(t *testing.T) {
	grpReset(t)
	s := &Server{}
	pa := provider.Provider{ID: "oa", Name: "OA", Chat: "http://127.0.0.1:1/v1", Key: "ka", KeyProtocol: provider.Chat}
	pb := provider.Provider{ID: "ob", Name: "OB", Chat: "http://127.0.0.1:1/v1", Key: "kb"}
	ms := []provider.Member{{Provider: pb, Model: "gpt-5.5"}, {Provider: pa, Model: "gpt-5.5"}}
	cs, pl, _ := s.planGroup(provider.Group{ID: "x", Routing: provider.Ordered}, ms, provider.Chat, &groupCall{group: "x"})
	if got, want := keyNames(cs, pl), []string{"ka", "kb aside"}; !equal(got, want) {
		t.Fatalf("order: %v, want %v", got, want)
	}
}

// A provider's affinity applies to a group that has none of its own, and the
// trace shows it as the mode.
func TestProviderAffinityReachesGroup(t *testing.T) {
	grpReset(t)
	s := &Server{}
	pa := provider.Provider{ID: "pf", Name: "PF", Chat: "http://127.0.0.1:1/v1", Key: "k1", KeyProtocol: provider.Chat,
		Affinity: provider.AffinitySession, Keys: []provider.KeyAccount{{Key: "k2", Protocol: provider.Chat}}}
	ms := []provider.Member{{Provider: pa, Model: "gpt-5.5"}}
	first, _, _ := s.planGroup(provider.Group{ID: "x"}, ms, provider.Chat, &groupCall{group: "x", session: "sess-pf"})
	gc := &groupCall{group: "x", session: "sess-pf", turns: 2}
	gc.answered(first[1], 0)
	cs, _, aff := s.planGroup(provider.Group{ID: "x"}, ms, provider.Chat, gc)
	// the decision is the member provider's session affinity, but the trace
	// shows the group's own setting, which is unset (I4)
	if aff == nil || aff.Mode != "" || aff.Why != "session" || !aff.Kept {
		t.Fatalf("affinity %+v, want group's own mode and session kept", aff)
	}
	if cs[0].rest != first[1].rest {
		t.Fatalf("first is %s, want the answerer %s", cs[0].rest, first[1].rest)
	}
}

// Provider rotation moves by the conversation's turns: a turn's tool results
// stay on the key the turn began with, and each new user turn moves on.
func TestProviderRotationByTurn(t *testing.T) {
	grpReset(t)
	s := &Server{}
	p := provider.Provider{ID: "rt", Name: "RT", Chat: "http://127.0.0.1:1/v1", Routing: provider.Rotate, Key: "k1",
		Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "k3"}}}
	const (
		turn = `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
		tool = `{"model":"m","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"out"}]}`
	)
	call := func(body string) string {
		h := http.Header{}
		h.Set("x-session-id", "sess-rt")
		gc := groupCallOf(h, provider.Chat, []byte(body), "")
		cs, _ := s.planFor(p, "m", provider.Chat, gc)
		return cs[0].p.Key
	}
	got := []string{call(turn), call(tool), call(turn), call(tool), call(turn), call(turn)}
	want := []string{"k1", "k1", "k2", "k2", "k3", "k1"}
	if !equal(got, want) {
		t.Fatalf("keys by turn: %v, want %v", got, want)
	}
}
