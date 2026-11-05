package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// xrTierNamesOf lists a plan's candidates as "key name (aside or not)" by key.
func xrTierNamesOf(cs []candidate, names map[string]string) []string {
	var out []string
	for _, c := range cs {
		n := names[c.rest]
		if c.Aside {
			n += " aside"
		}
		out = append(out, n)
	}
	return out
}

func TestXRKeysSplitByProtocolPools(t *testing.T) {
	s := &Server{}
	p := provider.Provider{ID: "tier-pool", Name: "Tier", Chat: "http://127.0.0.1:1/v1", Anthropic: "http://127.0.0.1:1",
		Key: "k-plain", Keys: []provider.KeyAccount{
			{Key: "k-chat", Protocol: provider.Chat},
			{Key: "k-ant", Protocol: provider.Anthropic},
			{Key: "k-plain2"},
		}}
	names := map[string]string{}
	for _, k := range []string{"k-plain", "k-chat", "k-ant", "k-plain2"} {
		names[p.ID+"#"+provider.KeyID(k)] = k
	}

	// a GPT model from a Chat agent: the Chat key is the pool; the unspecified
	// ones follow, then the Anthropic one, all marked aside
	cs, _ := s.plan(p, "gpt-5.5", provider.Chat)
	want := []string{"k-chat", "k-plain aside", "k-plain2 aside", "k-ant aside"}
	if got := xrTierNamesOf(cs, names); !xrEqual(got, want) {
		t.Fatalf("GPT from Chat: %v, want %v", got, want)
	}

	// a Claude model from a Chat agent: the model's own protocol decides
	cs, _ = s.plan(p, "claude-opus-5-5", provider.Chat)
	want = []string{"k-ant", "k-plain aside", "k-plain2 aside", "k-chat aside"}
	if got := xrTierNamesOf(cs, names); !xrEqual(got, want) {
		t.Fatalf("Claude from Chat: %v, want %v", got, want)
	}
	if cs[0].Aside {
		t.Fatal("the pool's first key is not aside")
	}
}

// Rotation goes among the keys that suit the request only, so a Chat
// request turns between the two Chat keys, and the others wait behind.
func TestXRRotationStaysInThePool(t *testing.T) {
	s := &Server{}
	p := provider.Provider{ID: "tier-rot", Name: "Rot", Chat: "http://127.0.0.1:1/v1", Routing: provider.Rotate,
		Key: "k-chat1", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{
			{Key: "k-plain"},
			{Key: "k-chat2", Protocol: provider.Chat},
		}}
	first := func() string {
		cs, _ := s.plan(p, "gpt-5.5", provider.Chat)
		return cs[0].p.Key + "/" + map[bool]string{true: "aside", false: "pool"}[cs[0].Aside]
	}
	if got := first(); got != "k-chat1/pool" {
		t.Fatalf("first turn: %s", got)
	}
	if got := first(); got != "k-chat2/pool" {
		t.Fatalf("second turn: %s", got)
	}
	if got := first(); got != "k-chat1/pool" {
		t.Fatalf("third turn: %s", got)
	}
}

func xrEqual(a, b []string) bool {
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
