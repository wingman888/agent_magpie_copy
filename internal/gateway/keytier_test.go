package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// keyTierProvider_f09 has an unnamed key, another unnamed one, and a Chat-only one.
func keyTierProvider_f09(id string) provider.Provider {
	return provider.Provider{ID: id, Name: "Tier", Chat: "https://tier.test/v1", Anthropic: "https://tier.test",
		Key: "k1", Routing: provider.Rotate,
		Keys: []provider.KeyAccount{{Key: "k2"}, {Key: "kc", Protocol: provider.Chat}}}
}

// keyNames_f09 tells candidates by their key's name, with * on those set aside.
func keyNames_f09(p provider.Provider, cs []candidate) string {
	names := map[string]string{provider.KeyID("k1"): "k1", provider.KeyID("k2"): "k2", provider.KeyID("kc"): "kc"}
	var out []string
	for _, c := range cs {
		n := names[strings.TrimPrefix(c.rest, p.ID+"#")]
		if c.aside {
			n += "*"
		}
		out = append(out, n)
	}
	return strings.Join(out, " ")
}

func TestKeysSetAsideByProtocol_f09(t *testing.T) {
	p := keyTierProvider_f09("tier-aside")
	// an Anthropic request: the unnamed keys are the pool, the Chat one is set aside
	cs, _ := perKeyOf(p, "m", provider.Anthropic)
	if got := keyNames_f09(p, cs); got != "k1 k2 kc*" {
		t.Fatalf("anthropic request: %s", got)
	}
	// a Chat request: the Chat key is the pool, the unnamed ones come after it
	cs, _ = perKeyOf(p, "m", provider.Chat)
	if got := keyNames_f09(p, cs); got != "kc k1* k2*" {
		t.Fatalf("chat request: %s", got)
	}
	// the request's protocol decides, whatever the model's name says
	cs, _ = perKeyOf(p, "claude-opus-5-5", provider.Chat)
	if got := keyNames_f09(p, cs); got != "kc k1* k2*" {
		t.Fatalf("claude from chat: %s", got)
	}
}

func TestRotationStaysInThePool_f09(t *testing.T) {
	p := keyTierProvider_f09("tier-turns")
	cs, _ := perKeyOf(p, "m", provider.Anthropic)
	var turns []string
	for i := 0; i < 3; i++ {
		ws, _ := weigh(p, cs, "m", provider.Anthropic)
		turns = append(turns, keyNames_f09(p, ws))
	}
	want := []string{"k1 k2 kc*", "k2 k1 kc*", "k1 k2 kc*"}
	for i := range want {
		if turns[i] != want[i] {
			t.Fatalf("turn %d: %s, want %s", i, turns[i], want[i])
		}
	}
}

func TestAsideIsLastResort_f09(t *testing.T) {
	p := keyTierProvider_f09("tier-last")
	s := &Server{}
	cs, _ := perKeyOf(p, "m", provider.Anthropic)
	// the pool fails: the key set aside is still tried, after it
	for _, c := range cs[:2] {
		s.restAfter(c, 402, nil, nil)
	}
	order, pl := s.plan(p, "m", provider.Anthropic)
	if got := keyNames_f09(p, order); got != "k1 k2 kc*" {
		t.Fatalf("with the pool resting: %s", got)
	}
	if len(pl.order) != 3 || !pl.order[2].Aside || pl.order[0].Rest == nil {
		t.Fatalf("trace: %+v", pl.order)
	}
}
