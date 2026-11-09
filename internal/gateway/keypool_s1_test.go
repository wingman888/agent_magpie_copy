package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Keys made for one protocol are pooled by it: the pool that suits the
// request is routed on its own, and the other keys come after it as asides,
// unspecified ones before another protocol's.
func TestS1KeyPoolProtocolTiers(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := provider.Provider{ID: "s1pool", Name: "S1", Chat: "http://s1.invalid/v1", Anthropic: "http://s1.invalid",
		Key: "s1-unspec", Keys: []provider.KeyAccount{
			{Key: "s1-chat", Protocol: provider.Chat},
			{Key: "s1-ant", Protocol: provider.Anthropic},
			{Key: "s1-unspec2"},
		}}
	// describe is each candidate's key and whether it is an aside
	describe := func(cs []candidate) string {
		var parts []string
		for _, c := range cs {
			s := c.p.Key
			if c.Aside {
				s += "(aside)"
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, " ")
	}

	// a Chat request: the Chat key is the pool; the unspecified keys follow,
	// then the Anthropic one
	cs, _ := perKeyOf(p, "gpt-5.5", provider.Chat)
	if got := describe(cs); got != "s1-chat s1-unspec(aside) s1-unspec2(aside) s1-ant(aside)" {
		t.Fatalf("chat request: %s", got)
	}

	// a Claude request is at home in Anthropic: the Anthropic key is the
	// pool, even from a Chat agent
	cs, _ = perKeyOf(p, "claude-opus-5-5", provider.Chat)
	if got := describe(cs); got != "s1-ant s1-unspec(aside) s1-unspec2(aside) s1-chat(aside)" {
		t.Fatalf("claude request: %s", got)
	}

	// in turn spreads over the pool only, as plan routes the pool before
	// the asides follow it
	p.Routing = provider.Rotate
	p.Keys = append(p.Keys, provider.KeyAccount{Key: "s1-chat2", Protocol: provider.Chat})
	var firsts []string
	for range 2 {
		cs, _ = perKeyOf(p, "gpt-5.5", provider.Chat)
		n := 0
		for n < len(cs) && !cs[n].Aside {
			n++
		}
		if n != 2 {
			t.Fatalf("pool of %d keys, want 2: %s", n, describe(cs))
		}
		routed := route(p, cs[:n], "gpt-5.5", provider.Chat)
		firsts = append(firsts, routed[0].p.Key)
	}
	if firsts[0] == firsts[1] {
		t.Fatalf("in turn, the same key led twice: %v", firsts)
	}
}
