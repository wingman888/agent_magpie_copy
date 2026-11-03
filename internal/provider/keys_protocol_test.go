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

func TestAddKeyProtocol(t *testing.T) {
	setupProtocolKeys(t)
	if err := AddKey("relay", "Bad", "sk-bad-0000", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if p, _ := Find("relay"); len(p.Keys) != 0 {
		t.Fatalf("a key with an unknown protocol was saved: %+v", p.Keys)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Any", "sk-any-3333", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 3 || ks[1].Protocol != Anthropic || ks[2].Protocol != "" {
		t.Fatalf("keys %+v", ks)
	}
	if p.Keys[0].Protocol != Anthropic || p.Keys[1].Protocol != "" {
		t.Fatalf("saved keys %+v", p.Keys)
	}
}

func TestSetKeyProtocolFirstKey(t *testing.T) {
	setupProtocolKeys(t)
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", "nope", Chat); err == nil {
		t.Fatal("a missing key was accepted")
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), "gemini-ish"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if ks := p.KeyList(); ks[0].Protocol != Chat {
		t.Fatalf("first key protocol %+v", ks)
	}
	// the limit follows the key when another one takes the lead
	if err := UseKey("relay", keyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	ks := p.KeyList()
	if !ks[0].Active || ks[0].Protocol != Anthropic || ks[1].Protocol != Chat || p.KeyProtocol != Anthropic {
		t.Fatalf("after use: %+v", ks)
	}
	// clearing the limit
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), ""); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.KeyList()[1].Protocol != "" {
		t.Fatalf("limit not cleared: %+v", p.KeyList())
	}
}

func TestProtocolFollowsKeyOnOffRemove(t *testing.T) {
	setupProtocolKeys(t)
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	// turning the first off promotes the Anthropic key; the Chat limit goes with its key
	if err := SetKeyOn("relay", keyID("sk-chat-1111"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-ant-2222" || p.KeyProtocol != Anthropic || p.Keys[0].Protocol != Chat {
		t.Fatalf("after off: %+v", p)
	}
	// back on, then removing the lead promotes the Chat key with its own limit
	if err := SetKeyOn("relay", keyID("sk-chat-1111"), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-chat-1111" || p.KeyProtocol != Chat || len(p.Keys) != 0 {
		t.Fatalf("after remove: %+v", p)
	}
}

func TestWithKeyFiltersEndpoints(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test", Key: "sk-first"}

	chatOnly := p.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chatOnly.Key != "sk-chat" || chatOnly.Chat == "" || chatOnly.Responses != "" || chatOnly.Anthropic != "" {
		t.Fatalf("chat key kept %+v", chatOnly)
	}
	unlimited := p.WithKey(KeyAccount{Key: "sk-any", Name: "Any"})
	if unlimited.Key != "sk-any" || unlimited.KeyName != "Any" || unlimited.Chat != p.Chat || unlimited.Responses != p.Responses || unlimited.Anthropic != p.Anthropic {
		t.Fatalf("unlimited key lost endpoints %+v", unlimited)
	}
	// the original is not changed
	if p.Key != "sk-first" || p.Chat == "" || p.Responses == "" || p.Anthropic == "" {
		t.Fatalf("original changed %+v", p)
	}
}
