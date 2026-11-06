package provider

import (
	"testing"
)

// The same model named differently by several vendors is one model.
func TestRgxNormalizeModelName(t *testing.T) {
	same := []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"anthropic/Claude-Opus-5.5-20260801",
	}
	for _, s := range same {
		if got := NormalizeModelName(s); got != "claude-opus-5-5" {
			t.Errorf("NormalizeModelName(%q) = %q, want claude-opus-5-5", s, got)
		}
	}
	if NormalizeModelName("x:batch") == NormalizeModelName("x") {
		t.Error("a colon variant must stay a different model")
	}
	if got := NormalizeModelName("x:batch"); got != "x:batch" {
		t.Errorf("colon variant rewritten: %q", got)
	}
	if got := NormalizeModelName("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta rewritten: %q", got)
	}
	if got := NormalizeModelName("gpt-5.5"); got != "gpt-5-5" {
		t.Errorf("gpt-5.5: %q", got)
	}
}

// Routing is one of the three, or smart; the old name for least used still
// reads.
func TestRgxNormalizeRouting(t *testing.T) {
	cases := map[string]string{
		"order": Ordered, "rotate": Rotate, "least-used": LeastUsed,
		"usage": LeastUsed, "": "", "ordered": "", "bogus": "",
	}
	for in, want := range cases {
		if got := NormalizeRouting(in); got != want {
			t.Errorf("NormalizeRouting(%q) = %q, want %q", in, got, want)
		}
	}
}

// Affinity is one of the four; anything else is automatic.
func TestRgxNormalizeAffinity(t *testing.T) {
	cases := map[string]string{
		"session": AffinitySession, "turn": AffinityTurn, "off": AffinityOff,
		"": AffinityAuto, "sticky": AffinityAuto,
	}
	for in, want := range cases {
		if got := NormalizeAffinity(in); got != want {
			t.Errorf("NormalizeAffinity(%q) = %q, want %q", in, got, want)
		}
	}
}

// A provider's affinity and routing are saved as given, or dropped when
// they're not known; "group" is no provider's id.
func TestRgxSaveNormalizesAndGroupID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "group", Name: "Group", Chat: "https://g.test/v1", Key: "sk-g"}); err == nil {
		t.Fatal(`a provider with id "group" was saved`)
	}
	if err := Save(Provider{Name: "group", Chat: "https://g.test/v1", Key: "sk-g"}); err == nil {
		t.Fatal(`a provider named group was saved`)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-1111",
		Affinity: "session", Routing: "usage"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Affinity != AffinitySession || p.Routing != LeastUsed {
		t.Fatalf("saved as %q %q", p.Affinity, p.Routing)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-1111",
		Affinity: "bogus", Routing: "bogus"}); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.Affinity != AffinityAuto || p.Routing != "" {
		t.Fatalf("invalid values not dropped: %q %q", p.Affinity, p.Routing)
	}
}

// Keys are ranked by how well they suit the request's protocol; a key with
// no protocol of its own is taken to be the provider's KeyProtocol when it
// is the first.
func TestRgxRankKeysByProtocol(t *testing.T) {
	keys := []KeyAccount{
		{Key: "k-any"},
		{Key: "k-chat", Protocol: Chat},
		{Key: "k-ant", Protocol: Anthropic},
		{Key: "k-chat2", Protocol: Chat},
	}
	primary, unspec, other := RankKeysByProtocol(keys, Chat)
	if len(primary) != 2 || primary[0].Key != "k-chat" || primary[1].Key != "k-chat2" {
		t.Fatalf("primary %+v", primary)
	}
	if len(unspec) != 1 || unspec[0].Key != "k-any" {
		t.Fatalf("unspecified %+v", unspec)
	}
	if len(other) != 1 || other[0].Key != "k-ant" {
		t.Fatalf("other %+v", other)
	}

	p := Provider{ID: "relay", Key: "sk-first", KeyProtocol: Anthropic, Keys: []KeyAccount{{Key: "sk-chat", Protocol: Chat}}}
	primary, unspec, other = p.KeysByProtocol(Anthropic)
	if len(primary) != 1 || primary[0].Key != "sk-first" || len(unspec) != 0 || len(other) != 1 || other[0].Key != "sk-chat" {
		t.Fatalf("first key takes the provider's protocol: %+v %+v %+v", primary, unspec, other)
	}
	primary, _, other = p.KeysByProtocol(Chat)
	if len(primary) != 1 || primary[0].Key != "sk-chat" || len(other) != 1 || other[0].Key != "sk-first" {
		t.Fatalf("for chat: %+v %+v", primary, other)
	}
}
