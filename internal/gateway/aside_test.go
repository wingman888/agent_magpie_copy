package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A provider whose keys are made for different protocols pools the keys that
// suit the request, and tries the others after it, marked aside.
func TestKeysOfOtherProtocolsAreAside(t *testing.T) {
	p := provider.Provider{ID: "aside-pool", Name: "Pool", Chat: "http://chat.invalid/v1", Anthropic: "http://ant.invalid",
		Key: "k-any-1", Keys: []provider.KeyAccount{
			{Key: "k-any-2"},
			{Key: "k-chat", Protocol: provider.Chat},
		}}
	var s Server
	cs, pl := s.plan(p, "gpt-5.5", provider.Chat)
	if len(cs) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(cs))
	}
	if want := "aside-pool#" + provider.KeyID("k-chat"); cs[0].rest != want || cs[0].aside {
		t.Fatalf("the Chat key is the pool: first %q aside=%v, want %q", cs[0].rest, cs[0].aside, want)
	}
	for _, c := range cs[1:] {
		if !c.aside {
			t.Fatalf("%q should be aside", c.rest)
		}
	}
	if len(pl.order) != 3 || pl.order[0].Aside || !pl.order[1].Aside || !pl.order[2].Aside {
		t.Fatalf("trace order: %+v", pl.order)
	}
}

// Rotation goes over the pool only: the keys of other protocols never take a
// turn ahead of it, and they stay aside.
func TestRotationStaysInPool(t *testing.T) {
	p := provider.Provider{ID: "aside-rotate", Name: "Rotate", Chat: "http://chat.invalid/v1", Anthropic: "http://ant.invalid",
		Routing: provider.Rotate, Key: "k-chat-1", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{
			{Key: "k-chat-2", Protocol: provider.Chat},
			{Key: "k-ant", Protocol: provider.Anthropic},
		}}
	var s Server
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		cs, _ := s.plan(p, "gpt-5.5", provider.Chat)
		if len(cs) != 3 || cs[0].aside || cs[1].aside || !cs[2].aside {
			t.Fatalf("round %d: the two Chat keys, then the Anthropic one aside: %+v", i, cs)
		}
		seen[cs[0].rest] = true
	}
	if len(seen) != 2 {
		t.Fatalf("the two Chat keys should take turns, saw %v", seen)
	}
}
