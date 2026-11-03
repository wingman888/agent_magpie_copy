package provider

import (
	"testing"
)

// twoEndpointRelay saves a relay with Chat and Anthropic bases and current key k1.
func twoEndpointRelay(t *testing.T, proto Protocol) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", KeyProtocol: proto,
		Chat: "https://relay.example.com/v1", Anthropic: "https://relay.example.com"}); err != nil {
		t.Fatal(err)
	}
}

func TestSetKeyProtocolSetsAndClears(t *testing.T) {
	twoEndpointRelay(t, "")
	if err := AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}

	if err := SetKeyProtocol("relay", KeyRef("k1"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if got := mustFind(t).KeyProtocol; got != Anthropic {
		t.Fatalf("current key protocol %q, want anthropic", got)
	}

	if err := SetKeyProtocol("relay", KeyRef("k2"), Chat); err != nil {
		t.Fatal(err)
	}
	if got := mustFind(t).Keys[0].Protocol; got != Chat {
		t.Fatalf("extra key protocol %q, want chat", got)
	}

	// clearing lifts the limit, on either key
	if err := SetKeyProtocol("relay", KeyRef("k1"), ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("k2"), ""); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if p.KeyProtocol != "" || p.Keys[0].Protocol != "" {
		t.Fatalf("limits not cleared: %q / %q", p.KeyProtocol, p.Keys[0].Protocol)
	}
}

func TestSetKeyProtocolRejectsUnknownAndMissing(t *testing.T) {
	twoEndpointRelay(t, "")
	for _, bad := range []Protocol{"bogus", Gemini, "Anthropic-ish"} {
		if err := SetKeyProtocol("relay", KeyRef("k1"), bad); err == nil {
			t.Errorf("protocol %q accepted", bad)
		}
	}
	if err := SetKeyProtocol("relay", "0000000000", Chat); err == nil {
		t.Error("unknown key ref accepted")
	}
	if got := mustFind(t).KeyProtocol; got != "" {
		t.Errorf("a rejected change still set %q", got)
	}
}

func TestSaveRejectsUnknownKeyProtocol(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", KeyProtocol: "bogus",
		Chat: "https://relay.example.com/v1"})
	if err == nil {
		t.Fatal("unknown keyProtocol saved")
	}
	err = Save(Provider{ID: "relay", Name: "Relay", Key: "k1",
		Keys: []ExtraKey{{Key: "k2", Protocol: "gemini"}}, Chat: "https://relay.example.com/v1"})
	if err == nil {
		t.Fatal("unknown extra key protocol saved")
	}
}

// A key limited to a protocol keeps only that protocol's base; a key whose
// protocol the provider lacks is left out.
func TestKeyCandidatesScopedToProtocol(t *testing.T) {
	twoEndpointRelay(t, Anthropic)
	if err := AddKey("relay", "resp", "k2", Responses); err != nil { // no Responses endpoint
		t.Fatal(err)
	}
	if err := AddKey("relay", "any", "k3", ""); err != nil {
		t.Fatal(err)
	}
	cands := mustFind(t).KeyCandidates()
	if len(cands) != 2 {
		t.Fatalf("got %d candidates, want the Anthropic key and the unlimited one", len(cands))
	}
	a := cands[0]
	if a.Key != "k1" || a.Chat != "" || a.Responses != "" || a.Anthropic == "" {
		t.Errorf("anthropic-limited candidate not scoped: %+v", a)
	}
	if got := a.Speaks(); len(got) != 1 || got[0] != Anthropic {
		t.Errorf("anthropic-limited candidate speaks %v", got)
	}
	u := cands[1]
	if u.Key != "k3" || u.Chat == "" || u.Anthropic == "" {
		t.Errorf("unlimited candidate lost endpoints: %+v", u)
	}
}

func TestSpeaksFollowsCurrentKeyLimit(t *testing.T) {
	twoEndpointRelay(t, Chat)
	if got := mustFind(t).Speaks(); len(got) != 1 || got[0] != Chat {
		t.Fatalf("speaks %v with a chat-limited key, want [chat]", got)
	}
	twoEndpointRelay(t, "")
	if got := mustFind(t).Speaks(); len(got) != 2 {
		t.Fatalf("speaks %v with no limit, want both", got)
	}
}
