package provider

import "testing"

func TestProtoLimit_UnknownRejectedAndNotSaved(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Bad", "sk-bad-2222", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	p, _ := Find("relay")
	if len(p.KeyList()) != 1 {
		t.Fatalf("a rejected key was saved: %+v", p.KeyList())
	}
	if err := AddKey("relay", "Ant", "sk-ant-3333", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic {
		t.Fatalf("added key protocol: %+v", ks)
	}
}

func TestProtoLimit_SetFirstKeyAndFollowSwitch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Ant", "sk-ant-3333", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-first-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if ks := p.KeyList(); ks[0].Protocol != Chat || ks[1].Protocol != Anthropic {
		t.Fatalf("after set: %+v", ks)
	}
	if err := SetKeyProtocol("relay", keyID("sk-first-1111"), "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was set")
	}
	if err := SetKeyProtocol("relay", keyID("sk-missing-9999"), Chat); err == nil {
		t.Fatal("a missing key was given a protocol")
	}

	// switch the current key: each limit goes with its key
	if err := UseKey("relay", keyID("sk-ant-3333")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-ant-3333" || p.KeyProtocol != Anthropic {
		t.Fatalf("new first key lost its limit: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-first-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("old key lost its limit: %+v", p.Keys)
	}
	if ks := p.KeyList(); ks[0].Protocol != Anthropic || ks[1].Protocol != Chat {
		t.Fatalf("list after switch: %+v", ks)
	}

	// clearing the limit on the first key
	if err := SetKeyProtocol("relay", keyID("sk-ant-3333"), ""); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.KeyProtocol != "" || p.KeyList()[0].Protocol != "" {
		t.Fatalf("limit not cleared: %+v", p.KeyList())
	}
}

func TestProtoLimit_WithKeyFiltersEndpoints(t *testing.T) {
	base := Provider{ID: "relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/v1", Anthropic: "https://relay.test"}

	limited := base.WithKey(KeyAccount{Key: "sk-chat-1111", Protocol: Chat})
	if limited.Key != "sk-chat-1111" || limited.KeyProtocol != Chat {
		t.Fatalf("key not swapped: %+v", limited)
	}
	if limited.Chat == "" || limited.Responses != "" || limited.Anthropic != "" {
		t.Fatalf("limited key kept other endpoints: %+v", limited)
	}
	if base.Chat == "" || base.Anthropic == "" || base.Key != "" {
		t.Fatalf("WithKey changed the receiver: %+v", base)
	}

	open := base.WithKey(KeyAccount{Key: "sk-any-2222"})
	if open.Chat == "" || open.Responses == "" || open.Anthropic == "" || open.KeyProtocol != "" {
		t.Fatalf("unlimited key dropped endpoints: %+v", open)
	}
}

// A limit travels with its key through turning off, turning back on, and
// removing the keys around it.
func TestProtoLimit_FollowsKeyThroughOffAndRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test",
		Key: "sk-first-1111", KeyProtocol: Chat,
		Keys: []KeyAccount{{Key: "sk-ant-3333", Protocol: Anthropic}, {Key: "sk-any-4444"}}}); err != nil {
		t.Fatal(err)
	}

	// turn the Chat-limited first key off: the Anthropic one is promoted, and
	// the off key keeps its Chat limit
	if err := SetKeyOn("relay", keyID("sk-first-1111"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-ant-3333" || p.KeyProtocol != Anthropic {
		t.Fatalf("promoted key lost its limit: %q %q", p.Key, p.KeyProtocol)
	}
	ks := p.KeyList()
	if len(ks) != 3 || ks[1].ID != keyID("sk-first-1111") || ks[1].Protocol != Chat || ks[1].On ||
		ks[2].ID != keyID("sk-any-4444") || ks[2].Protocol != "" {
		t.Fatalf("list after off: %+v", ks)
	}

	// turn it back on: still Chat-limited, in its place
	if err := SetKeyOn("relay", keyID("sk-first-1111"), true); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if ks := p.KeyList(); ks[1].Protocol != Chat || !ks[1].On || ks[2].Protocol != "" {
		t.Fatalf("list after on: %+v", ks)
	}

	// remove the Chat-limited backup: the others keep their own limits
	if err := RemoveKey("relay", keyID("sk-first-1111")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-ant-3333" || p.KeyProtocol != Anthropic {
		t.Fatalf("after removing backup, first: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-any-4444" || p.Keys[0].Protocol != "" {
		t.Fatalf("after removing backup, backups: %+v", p.Keys)
	}

	// remove the current Anthropic-limited key: the unlimited one takes its
	// place, with no limit
	if err := RemoveKey("relay", keyID("sk-ant-3333")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-any-4444" || p.KeyProtocol != "" || len(p.Keys) != 0 {
		t.Fatalf("after removing first: %+v", p.KeyList())
	}
}
