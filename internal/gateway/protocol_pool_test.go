package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Two keys named no protocol and one made for Chat, the request is Anthropic:
// the two that name none are the pool and rotate; the Chat one comes last,
// aside, and rotation never reaches it.
func TestProtocolPoolKeepsAsidesBehind(t *testing.T) {
	p := provider.Provider{ID: "pool-rot", Name: "Pool", Chat: "https://pool.test/v1", Anthropic: "https://pool.test",
		Key: "k-one", Routing: provider.Rotate,
		Keys: []provider.KeyAccount{{Key: "k-two"}, {Key: "k-chat", Protocol: provider.Chat}}}
	cs := perKey(p, "m", provider.Anthropic)
	if len(cs) != 3 || cs[0].aside || cs[1].aside || !cs[2].aside || cs[2].p.KeyProtocol != provider.Chat {
		t.Fatalf("order %q", restsOf(cs))
	}
	var firsts []string
	for range 4 {
		out := route(p, cs, "m", provider.Anthropic)
		if len(out) != 3 || out[0].aside || out[1].aside || !out[2].aside {
			t.Fatalf("aside not last: %q", restsOf(out))
		}
		firsts = append(firsts, out[0].rest)
	}
	if firsts[0] == firsts[1] || firsts[0] != firsts[2] || firsts[1] != firsts[3] {
		t.Fatalf("rotation didn't turn among the pool: %v", firsts)
	}
	for _, f := range firsts {
		if f == cs[2].rest {
			t.Fatalf("rotation reached the key made for another protocol: %v", firsts)
		}
	}
}

// The best tier present is the pool: a key made for the request's protocol
// comes first, the other keys after it.
func TestProtocolPoolBestTierPresent(t *testing.T) {
	p := provider.Provider{ID: "pool-tier", Name: "Pool", Chat: "https://pool.test/v1", Anthropic: "https://pool.test",
		Key: "k-one", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{{Key: "k-two", Protocol: provider.Anthropic}}}
	cs := perKey(p, "claude-opus-5-5", provider.Anthropic)
	if len(cs) != 2 || cs[0].aside || cs[0].p.KeyProtocol != provider.Anthropic || !cs[1].aside || cs[1].p.KeyProtocol != provider.Chat {
		t.Fatalf("order %q", restsOf(cs))
	}
	// a request no key was made for: the only keys there are are the pool
	lone := []candidate{{p: provider.Provider{ID: "x", KeyProtocol: provider.Chat}, rest: "x"}}
	if main, aside := protocolPool(lone, provider.Anthropic); len(main) != 1 || len(aside) != 0 {
		t.Fatalf("a lone key is the pool: %d main, %d aside", len(main), len(aside))
	}
}

// Keys naming no protocol come before those made for another, and the trace
// says which are aside.
func TestProtocolPoolUnspecifiedBeforeOther(t *testing.T) {
	p := provider.Provider{ID: "pool-un", Name: "Pool", Chat: "https://pool.test/v1", Anthropic: "https://pool.test",
		Key: "k-ant", KeyProtocol: provider.Anthropic,
		Keys: []provider.KeyAccount{{Key: "k-any"}, {Key: "k-chat", Protocol: provider.Chat}}}
	cs := perKey(p, "gpt-5.5", provider.Anthropic)
	if len(cs) != 3 || cs[0].aside || !cs[1].aside || cs[1].p.KeyProtocol != "" || !cs[2].aside || cs[2].p.KeyProtocol != provider.Chat {
		t.Fatalf("order %q", restsOf(cs))
	}
	if w := weighed(cs[2], cs[2].p, weighing{}, false, provider.Anthropic); !w.Aside {
		t.Fatal("the trace doesn't say aside")
	}
	if w := weighed(cs[0], cs[0].p, weighing{}, false, provider.Anthropic); w.Aside {
		t.Fatal("the pool is marked aside")
	}
}
