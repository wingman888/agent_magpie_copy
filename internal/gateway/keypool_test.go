package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// keyPoolSetup clears the rests and keeps the config off the disk.
func keyPoolSetup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
}

// Keys made for Anthropic (k1, k2) and for Chat (kc1, kc2), rotating: a
// Claude request turns the Anthropic pool and a GPT request the Chat pool,
// and each goes on from where it left off whatever the other one did.
func TestRotateTurnsEachProtocolPoolApart(t *testing.T) {
	keyPoolSetup(t)
	p := provider.Provider{ID: "pool-rotate", Routing: provider.Rotate,
		Chat: "http://relay.test/v1", Anthropic: "http://relay.test", Models: []string{"m", "claude-x"},
		Key: "k1", KeyProtocol: provider.Anthropic,
		Keys: []provider.KeyAccount{{Key: "k2", Protocol: provider.Anthropic}, {Key: "kc1", Protocol: provider.Chat}, {Key: "kc2", Protocol: provider.Chat}}}
	rest := func(k string) string { return "pool-rotate#" + provider.KeyID(k) }
	s := New()
	// the first key of each request's turn: the Anthropic pool and the Chat
	// pool each take their turns in turn, k1→k2→k1 and kc1→kc2→kc1
	for i, c := range []struct {
		model string
		from  provider.Protocol
		first string
	}{
		{"claude-x", provider.Chat, rest("k1")},
		{"m", provider.Chat, rest("kc1")},
		{"claude-x", provider.Anthropic, rest("k2")},
		{"m", provider.Chat, rest("kc2")},
		{"claude-x", provider.Chat, rest("k1")},
		{"m", provider.Chat, rest("kc1")},
	} {
		cs, _ := s.plan(p, c.model, c.from)
		if cs[0].rest != c.first {
			t.Fatalf("request %d (%s from %s): first %s, want %s", i, c.model, c.from, cs[0].rest, c.first)
		}
	}
}

// The first key with no protocol of its own is made for the provider's
// KeyProtocol, as KeysOn reads it: here k1 is made for Chat, so an Anthropic
// request takes k2 and k1 aside, and a Chat request the other way round.
func TestFirstKeyTakesTheProviderKeyProtocol(t *testing.T) {
	keyPoolSetup(t)
	p := provider.Provider{ID: "fallback-proto", Chat: "http://relay.test/v1", Anthropic: "http://relay.test",
		Models: []string{"m"}, Key: "k1", KeyProtocol: provider.Chat, Keys: []provider.KeyAccount{{Key: "k2"}}}
	if ks := p.KeysOn(); len(ks) != 2 || ks[0].Protocol != provider.Chat || ks[1].Protocol != "" {
		t.Fatalf("KeysOn: %+v, want the first key made for Chat and the second for any", ks)
	}
	k1, k2 := "fallback-proto#"+provider.KeyID("k1"), "fallback-proto#"+provider.KeyID("k2")
	s := New()
	for _, c := range []struct {
		from provider.Protocol
		want string // in order; "*" marks one aside
	}{
		{provider.Anthropic, k2 + " " + k1 + "* "},
		{provider.Chat, k1 + " " + k2 + "* "},
	} {
		cs, pl := s.plan(p, "m", c.from)
		var got string
		for i, cand := range cs {
			got += cand.rest
			if cand.aside {
				got += "*"
			}
			got += " "
			if w := pl.order[i]; w.ID != cand.rest || w.Aside != cand.aside {
				t.Fatalf("%s: trace says %s aside=%v, candidate is %s aside=%v", c.from, w.ID, w.Aside, cand.rest, cand.aside)
			}
			if w := pl.order[i]; w.Kind == "key" && cand.rest == k1 && w.Speaks != provider.Chat {
				t.Fatalf("%s: trace says the first key speaks %q, want %q", c.from, w.Speaks, provider.Chat)
			}
		}
		if got != c.want {
			t.Errorf("%s request: got %q, want %q", c.from, got, c.want)
		}
	}
}

// Keys aside never take a turn: with two keys in the pool that suits the
// request, rotating, the turn goes to the first of them in turn, and no
// aside key is marked as having it.
func TestAsideKeysDoNotTakeTheTurn(t *testing.T) {
	keyPoolSetup(t)
	p := provider.Provider{ID: "aside-turn", Routing: provider.Rotate,
		Chat: "http://relay.test/v1", Anthropic: "http://relay.test", Models: []string{"m"},
		Key: "kc1", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{{Key: "kc2", Protocol: provider.Chat}, {Key: "ka1", Protocol: provider.Anthropic}}}
	kc1, kc2 := "aside-turn#"+provider.KeyID("kc1"), "aside-turn#"+provider.KeyID("kc2")
	s := New()
	for i, first := range []string{kc1, kc2, kc1} {
		cs, pl := s.plan(p, "m", provider.Chat)
		if cs[0].rest != first {
			t.Fatalf("turn %d: first %s, want %s", i, cs[0].rest, first)
		}
		var turns []int
		for j, w := range pl.order {
			if w.Turn {
				turns = append(turns, j)
				if w.Aside {
					t.Fatalf("turn %d: an aside key %s holds the turn", i, w.ID)
				}
			}
		}
		if len(turns) != 1 || turns[0] != 0 {
			t.Fatalf("turn %d: turn marked at %v, want only the first of the pool", i, turns)
		}
		if last := pl.order[len(pl.order)-1]; !last.Aside || last.Turn {
			t.Fatalf("turn %d: last is %s aside=%v turn=%v, want the Anthropic key aside", i, last.ID, last.Aside, last.Turn)
		}
	}
}

// A pool of one key takes no turn at all, however many keys are aside.
func TestSingleMainKeyTakesNoTurn(t *testing.T) {
	keyPoolSetup(t)
	p := provider.Provider{ID: "single-turn", Routing: provider.Rotate,
		Chat: "http://relay.test/v1", Anthropic: "http://relay.test", Models: []string{"m"},
		Key: "kc1", KeyProtocol: provider.Chat,
		Keys: []provider.KeyAccount{{Key: "ka1", Protocol: provider.Anthropic}, {Key: "ka2", Protocol: provider.Anthropic}}}
	s := New()
	for range 3 {
		_, pl := s.plan(p, "m", provider.Chat)
		for _, w := range pl.order {
			if w.Turn {
				t.Fatalf("%s holds a turn in a pool of one", w.ID)
			}
		}
	}
}
