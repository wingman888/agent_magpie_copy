package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Keys made for another protocol than the request's wait behind the ones
// that suit it, and the ones that suit rotate among themselves.
func TestS1KeyPoolByProtocol(t *testing.T) {
	p := provider.Provider{ID: "s1pool", Chat: "https://s1.example/v1", Anthropic: "https://s1.example",
		Key: "s1-k1", Keys: []provider.KeyAccount{{Key: "s1-k2", Protocol: provider.Chat}, {Key: "s1-k3"}}}
	p.KeyProtocol = provider.Chat // k1 is a Chat key too; k3 is unspecified
	const model = "s1-plain-model"
	ids := func(cs []candidate) (out []string) {
		for _, c := range cs {
			out = append(out, c.p.Key)
		}
		return out
	}

	cs := perKey(p, model, provider.Chat)
	if got := ids(cs); len(got) != 3 || got[0] != "s1-k1" || got[1] != "s1-k2" || got[2] != "s1-k3" {
		t.Fatalf("pool order: %v", got)
	}
	if cs[0].aside || cs[1].aside || !cs[2].aside {
		t.Fatalf("aside: %v %v %v, want false false true", cs[0].aside, cs[1].aside, cs[2].aside)
	}

	// the unspecified key is tried only after the two that suit, and it
	// stays last however the pool is routed
	p.Routing = provider.Rotate
	for n, want := range [][]string{{"s1-k1", "s1-k2", "s1-k3"}, {"s1-k2", "s1-k1", "s1-k3"}, {"s1-k1", "s1-k2", "s1-k3"}} {
		routed, _ := weigh(p, perKey(p, model, provider.Chat), model, provider.Chat)
		if got := ids(routed); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("turn %d: %v, want %v", n, got, want)
		}
	}
}

// The candidates of a provider list the asides after the main pool, and
// the trace marks them.
func TestS1AsideCandidates(t *testing.T) {
	p := provider.Provider{ID: "s1aside", Chat: "https://s1.example/v1", Anthropic: "https://s1.example",
		Key: "s1-only-anthropic", KeyProtocol: provider.Anthropic,
		Keys: []provider.KeyAccount{{Key: "s1-chat", Protocol: provider.Chat}}}
	s := &Server{}
	// a Chat request for a model no family names: the Chat key suits it
	cs, pl := s.plan(p, "s1-plain-model", provider.Chat)
	if len(cs) != 2 || cs[0].p.Key != "s1-chat" || cs[0].aside || cs[1].p.Key != "s1-only-anthropic" || !cs[1].aside {
		t.Fatalf("candidates: %+v", cs)
	}
	if len(pl.order) != 2 || pl.order[0].Aside || !pl.order[1].Aside {
		t.Fatalf("trace: %+v", pl.order)
	}
	// a Claude model is at home in the Anthropic key, whatever the request spoke
	cs, _ = s.plan(p, "claude-s1", provider.Chat)
	if len(cs) != 2 || cs[0].p.Key != "s1-only-anthropic" || cs[1].p.Key != "s1-chat" || !cs[1].aside {
		t.Fatalf("claude candidates: %+v", cs)
	}
}
