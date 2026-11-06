package provider

import "testing"

func TestKeyProtocolAddAndList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Bad", "sk-bad-3333", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if p, _ := Find("relay"); len(p.Keys) != 0 {
		t.Fatalf("a rejected key was saved: %+v", p.Keys)
	}
	if err := AddKey("relay", "Claude", "sk-claude-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	ks := mustFind(t, "relay").KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic || ks[0].Protocol != "" {
		t.Fatalf("keys %+v", ks)
	}
}

func TestKeyProtocolSetFollowsKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-claude-2222", ""); err != nil {
		t.Fatal(err)
	}
	first := keyID("sk-first-1111")
	claude := keyID("sk-claude-2222")
	if err := SetKeyProtocol("relay", first, Chat); err != nil {
		t.Fatal(err)
	}
	if ks := mustFind(t, "relay").KeyList(); ks[0].Protocol != Chat {
		t.Fatalf("first key protocol not shown: %+v", ks)
	}
	if err := SetKeyProtocol("relay", first, "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if err := SetKeyProtocol("relay", "ffffffffff", Chat); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if err := SetKeyProtocol("relay", claude, Anthropic); err != nil {
		t.Fatal(err)
	}
	// switch: the limits travel with their keys
	if err := UseKey("relay", claude); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, "relay")
	if p.Key != "sk-claude-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("new first key lost its limit: %+v", p)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-first-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("old first key lost its limit: %+v", p.Keys)
	}
	// clearing lifts the limit
	if err := SetKeyProtocol("relay", claude, ""); err != nil {
		t.Fatal(err)
	}
	if p = mustFind(t, "relay"); p.KeyProtocol != "" {
		t.Fatalf("limit not cleared: %+v", p.KeyProtocol)
	}
}

func TestWithKeyKeepsOnlyKeyProtocolEndpoint(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test"}
	q := p.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if q.Chat == "" || q.Responses != "" || q.Anthropic != "" || q.Key != "sk-chat" {
		t.Fatalf("limited key kept %+v", q)
	}
	q = p.WithKey(KeyAccount{Key: "sk-any"})
	if q.Chat == "" || q.Responses == "" || q.Anthropic == "" || q.Key != "sk-any" {
		t.Fatalf("unlimited key lost endpoints %+v", q)
	}
	if p.Chat == "" || p.Anthropic == "" || p.Key != "" {
		t.Fatalf("WithKey changed the receiver: %+v", p)
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
