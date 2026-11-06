package provider

import "testing"

func setupProtocolKeys(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
}

func TestAddKeyRejectsUnknownProtocol(t *testing.T) {
	setupProtocolKeys(t)
	if err := AddKey("relay", "Pigeon", "sk-pigeon-3333", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if p, _ := Find("relay"); len(p.KeyList()) != 1 {
		t.Fatalf("a rejected key was saved: %+v", p.KeyList())
	}
}

func TestAddKeyProtocolShowsInList(t *testing.T) {
	setupProtocolKeys(t)
	if err := AddKey("relay", "Claude", "sk-claude-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[0].Protocol != "" || ks[1].Protocol != Anthropic {
		t.Fatalf("key list %+v", ks)
	}
}

func TestSetKeyProtocolFollowsKeyOnSwitch(t *testing.T) {
	setupProtocolKeys(t)
	if err := AddKey("relay", "Claude", "sk-claude-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if ks := p.KeyList(); ks[0].Protocol != Chat || ks[1].Protocol != Anthropic {
		t.Fatalf("after set: %+v", ks)
	}
	if err := UseKey("relay", keyID("sk-claude-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	ks := p.KeyList()
	if !ks[0].Active || ks[0].Protocol != Anthropic || ks[1].Protocol != Chat {
		t.Fatalf("after switch: %+v", ks)
	}
	// clearing the first key's limit works too
	if err := SetKeyProtocol("relay", keyID("sk-claude-2222"), ""); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.KeyProtocol != "" {
		t.Fatalf("limit not cleared: %q", p.KeyProtocol)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), "carrier-pigeon"); err == nil {
		t.Fatal("unknown protocol accepted by SetKeyProtocol")
	}
	if err := SetKeyProtocol("relay", "ffffffffff", Chat); err == nil {
		t.Fatal("unknown key accepted by SetKeyProtocol")
	}
}

func TestProtocolFollowsPromoteAndRemove(t *testing.T) {
	setupProtocolKeys(t)
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-claude-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	// turning the first key off hands the lead to the Anthropic key
	if err := SetKeyOn("relay", keyID("sk-chat-1111"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-claude-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("after off: key %q protocol %q", p.Key, p.KeyProtocol)
	}
	if ks := p.KeyList(); ks[1].Protocol != Chat {
		t.Fatalf("switched-off key lost its protocol: %+v", ks)
	}
	// removing a saved key takes its protocol with it
	if err := RemoveKey("relay", keyID("sk-chat-1111")); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.KeyProtocol != Anthropic || len(p.Keys) != 0 {
		t.Fatalf("after remove: %+v", p)
	}
}

func TestWithKeyFiltersEndpoints(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test"}

	chatOnly := p.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chatOnly.Chat == "" || chatOnly.Responses != "" || chatOnly.Anthropic != "" || chatOnly.Key != "sk-chat" {
		t.Fatalf("chat-limited copy: %+v", chatOnly)
	}
	if p.Responses == "" || p.Anthropic == "" {
		t.Fatal("WithKey changed the original provider")
	}

	all := p.WithKey(KeyAccount{Key: "sk-any"})
	if all.Chat != p.Chat || all.Responses != p.Responses || all.Anthropic != p.Anthropic {
		t.Fatalf("unlimited copy lost endpoints: %+v", all)
	}
}
