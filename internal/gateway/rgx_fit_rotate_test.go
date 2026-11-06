package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A Chat request for a Claude model, with only a Chat-made key and a key
// naming no protocol: no key is made for Anthropic, so the request's own
// protocol decides. The Chat key is the pool; the unnamed one follows, aside.
func TestRgxFitChatRequestClaudeModel(t *testing.T) {
	p := provider.Provider{ID: "rgx-chat-only", Name: "Rgx", Chat: "https://rgx-chat.test/v1", Anthropic: "https://rgx-chat.test",
		Key: "rgx-any", Keys: []provider.KeyAccount{{Key: "rgx-chat", Protocol: provider.Chat}}}
	cs := perKey(p, "claude-opus-4-8", provider.Chat)
	if len(cs) != 2 {
		t.Fatalf("candidates %q", restsOf(cs))
	}
	if cs[0].aside || cs[0].p.KeyProtocol != provider.Chat {
		t.Fatalf("Chat key not in the pool first: %q", restsOf(cs))
	}
	if !cs[1].aside || cs[1].p.KeyProtocol != "" {
		t.Fatalf("unnamed key not behind the pool as aside: %q aside=%v", restsOf(cs), cs[1].aside)
	}
	if got := keyFit(cs[0], provider.Chat); got != 0 {
		t.Fatalf("Chat key fit %d, want 0", got)
	}
	// the pool's key leads every time, the aside one never moves ahead of it
	for range 3 {
		out := route(p, cs, "claude-opus-4-8", provider.Chat)
		if len(out) != 2 || out[0].rest != cs[0].rest || out[0].aside || !out[1].aside {
			t.Fatalf("routed %q", restsOf(out))
		}
	}
}

// When a key made for Anthropic is present, a Claude model still goes to it
// over a Chat request, and the Chat key is the aside one.
func TestRgxFitAnthropicKeyPresentForClaude(t *testing.T) {
	p := provider.Provider{ID: "rgx-both", Name: "Rgx", Chat: "https://rgx-both.test/v1", Anthropic: "https://rgx-both.test",
		Key: "rgx-oai", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{{Key: "rgx-ant", Protocol: provider.Anthropic}}}
	cs := perKey(p, "claude-opus-4-8", provider.Chat)
	if len(cs) != 2 || cs[0].p.KeyProtocol != provider.Anthropic || cs[0].aside {
		t.Fatalf("Anthropic key not first in the pool: %q", restsOf(cs))
	}
	if !cs[1].aside {
		t.Fatalf("Chat key not aside: %q", restsOf(cs))
	}
}

// Rotation within a turn, whose starting key has been taken out: the turn's
// order is kept, and no rotation is moved on or restarted.
func TestRgxRotateWithinTurnStartGone(t *testing.T) {
	p := provider.Provider{ID: "rgx-rot", Name: "Rgx", Chat: "https://rgx-rot.test/v1", Anthropic: "https://rgx-rot.test",
		Key: "rgx-one", Routing: provider.Rotate, Keys: []provider.KeyAccount{{Key: "rgx-two"}}}
	cs := perKey(p, "m", provider.Chat)
	if len(cs) != 2 {
		t.Fatalf("candidates %q", restsOf(cs))
	}
	turnKey := p.ID + "\x00m"
	routed.Lock()
	delete(routed.turn, turnKey)
	routed.Unlock()

	gone := candidate{rest: "rgx-rot#gone", model: "m"}
	rc := routeCtx{key: "rgx-within\x00m", model: "m", turns: 2, within: true,
		pins: map[string]bool{candKey(gone): true}}
	got, _ := weighCtx(p, cs, "m", provider.Chat, rc)
	if restsOf(got) != restsOf(cs) {
		t.Fatalf("within a turn, order changed: %q, want %q", restsOf(got), restsOf(cs))
	}
	routed.Lock()
	_, moved := routed.turn[turnKey]
	routed.Unlock()
	if moved {
		t.Fatalf("rotation advanced by a request within a turn whose start is gone")
	}

	// a new turn then starts the rotation where it was: first key, then the next
	newTurn := routeCtx{key: "rgx-within\x00m", model: "m", turns: 3}
	first, _ := weighCtx(p, cs, "m", provider.Chat, newTurn)
	second, _ := weighCtx(p, cs, "m", provider.Chat, routeCtx{key: "rgx-within\x00m", model: "m", turns: 4})
	if first[0].rest != cs[0].rest || second[0].rest != cs[1].rest {
		t.Fatalf("new turns did not rotate from the start: %q then %q", restsOf(first), restsOf(second))
	}
}
