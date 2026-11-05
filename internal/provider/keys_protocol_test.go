package provider

import "testing"

func TestAddKeyProtocol(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Bad", "sk-pigeon-3333", "carrier-pigeon"); err == nil {
		t.Fatal("unknown protocol was accepted")
	}
	if p, _ := Find("relay"); len(p.Keys) != 0 {
		t.Fatalf("a key with an unknown protocol was saved: %+v", p.Keys)
	}
	if err := AddKey("relay", "Claude", "sk-claude-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic || ks[0].Protocol != "" {
		t.Fatalf("key list %+v", ks)
	}
}

func TestSetKeyProtocolFollowsKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-claude-2222", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-first-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	if ks := mustFind(t, "relay").KeyList(); ks[0].Protocol != Chat {
		t.Fatalf("first key protocol %+v", ks)
	}
	if err := SetKeyProtocol("relay", keyID("sk-claude-2222"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-claude-2222"), "carrier-pigeon"); err == nil {
		t.Fatal("unknown protocol was accepted")
	}
	if err := SetKeyProtocol("relay", "ffffffffff", Chat); err == nil {
		t.Fatal("unknown key was accepted")
	}

	// switching the first key carries each limit with its key
	if err := UseKey("relay", keyID("sk-claude-2222")); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, "relay")
	if p.Key != "sk-claude-2222" || p.KeyProtocol != Anthropic || p.Keys[0].Protocol != Chat || p.Keys[0].Key != "sk-first-1111" {
		t.Fatalf("after use: %+v", p)
	}
	ks := p.KeyList()
	if ks[0].Protocol != Anthropic || ks[1].Protocol != Chat {
		t.Fatalf("key list after use %+v", ks)
	}

	// turning the first key off moves its limit to the key that takes over
	if err := SetKeyOn("relay", keyID("sk-claude-2222"), false); err != nil {
		t.Fatal(err)
	}
	p = mustFind(t, "relay")
	if p.Key != "sk-first-1111" || p.KeyProtocol != Chat || p.Keys[0].Protocol != Anthropic || !p.Keys[0].Off {
		t.Fatalf("after off: %+v", p)
	}

	// clearing the limit
	if err := SetKeyProtocol("relay", keyID("sk-first-1111"), ""); err != nil {
		t.Fatal(err)
	}
	if p = mustFind(t, "relay"); p.KeyProtocol != "" || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("after clear: %+v", p)
	}

	// removing a key drops its own limit only
	if err := SetKeyOn("relay", keyID("sk-claude-2222"), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID("sk-claude-2222")); err != nil {
		t.Fatal(err)
	}
	if p = mustFind(t, "relay"); p.Key != "sk-first-1111" || p.KeyProtocol != "" || len(p.Keys) != 0 {
		t.Fatalf("after remove: %+v", p)
	}
}

func TestWithKeyFiltersEndpoints(t *testing.T) {
	p := Provider{ID: "relay", Key: "sk-first-1111", Chat: "https://relay.test/v1", Responses: "https://relay.test/v1", Anthropic: "https://relay.test"}

	chatOnly := p.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chatOnly.Key != "sk-chat" || chatOnly.Chat == "" || chatOnly.Responses != "" || chatOnly.Anthropic != "" {
		t.Fatalf("chat-limited key: %+v", chatOnly)
	}
	if p.Responses == "" || p.Anthropic == "" || p.Key != "sk-first-1111" {
		t.Fatalf("WithKey changed the original: %+v", p)
	}

	all := p.WithKey(KeyAccount{Key: "sk-any"})
	if all.Key != "sk-any" || all.Chat == "" || all.Responses == "" || all.Anthropic == "" {
		t.Fatalf("unlimited key: %+v", all)
	}
}

func mustFind(t *testing.T, id string) *Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
