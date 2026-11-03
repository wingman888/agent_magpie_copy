package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func setupKeyProtocolTest(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestAddKeyProtocol(t *testing.T) {
	setupKeyProtocolTest(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Bad", "sk-bad-3333", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if p, _ := Find("relay"); len(p.Keys) != 0 {
		t.Fatalf("a rejected key was saved: %+v", p.Keys)
	}
	if err := AddKey("relay", "Claude", "sk-claude-4444", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic {
		t.Fatalf("key list %+v", ks)
	}
	if ks[0].Protocol != "" {
		t.Fatalf("the first key should be unlimited: %+v", ks[0])
	}
}

func TestSetKeyProtocolFirstKey(t *testing.T) {
	setupKeyProtocolTest(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-1111", KeyName: "First"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Second", "sk-second-2222", ""); err != nil {
		t.Fatal(err)
	}
	first := KeyID("sk-first-1111")
	second := KeyID("sk-second-2222")

	if err := SetKeyProtocol("relay", first, Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.KeyProtocol != Chat || p.KeyList()[0].Protocol != Chat {
		t.Fatalf("first key protocol not set: %+v", p.KeyList())
	}

	if err := SetKeyProtocol("relay", second, Anthropic); err != nil {
		t.Fatal(err)
	}
	// make the second key current: its limit follows it, and the first
	// key's limit follows the first key into the list
	if err := UseKey("relay", second); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-second-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("new current key lost its limit: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-first-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("old current key lost its limit: %+v", p.Keys)
	}
	ks := p.KeyList()
	if ks[0].Protocol != Anthropic || ks[1].Protocol != Chat {
		t.Fatalf("key list protocols %+v", ks)
	}

	if err := SetKeyProtocol("relay", second, "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if err := SetKeyProtocol("relay", "00000000ff", Chat); err == nil {
		t.Fatal("an unknown key was accepted")
	}
	// clearing lifts the limit
	if err := SetKeyProtocol("relay", second, ""); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.KeyProtocol != "" {
		t.Fatalf("limit not cleared: %q", p.KeyProtocol)
	}
}

func TestKeyProtocolFollowsKeyThroughOffAndRemove(t *testing.T) {
	setupKeyProtocolTest(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-a-1111", KeyProtocol: Chat}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "B", "sk-b-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "C", "sk-c-3333", Responses); err != nil {
		t.Fatal(err)
	}
	// turn the first key off: the next one on, B, takes the lead with its own limit
	if err := SetKeyOn("relay", KeyID("sk-a-1111"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-b-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("promote did not carry B's limit: %q %q", p.Key, p.KeyProtocol)
	}
	var aOff KeyAccount
	for _, k := range p.Keys {
		if k.Key == "sk-a-1111" {
			aOff = k
		}
	}
	if !aOff.Off || aOff.Protocol != Chat {
		t.Fatalf("A lost its limit when turned off: %+v", aOff)
	}
	// turn A back on: it stays in the list with its limit
	if err := SetKeyOn("relay", KeyID("sk-a-1111"), true); err != nil {
		t.Fatal(err)
	}
	// remove B (current): C takes over with its own limit
	if err := RemoveKey("relay", KeyID("sk-b-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	// A was turned back on, so it is next in line and takes over
	if p.Key != "sk-a-1111" || p.KeyProtocol != Chat {
		t.Fatalf("after removing B: %q %q", p.Key, p.KeyProtocol)
	}
	for _, k := range p.KeysOn() {
		want := map[string]Protocol{"sk-a-1111": Chat, "sk-c-3333": Responses}[k.Key]
		if k.Protocol != want {
			t.Fatalf("key %s has protocol %q, want %q", k.Key, k.Protocol, want)
		}
	}
	if err := RemoveKey("relay", KeyID("sk-a-1111")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-c-3333" || p.KeyProtocol != Responses || len(p.Keys) != 0 {
		t.Fatalf("after removing A: %q %q %+v", p.Key, p.KeyProtocol, p.Keys)
	}
}

func TestKeyProtocolJSON(t *testing.T) {
	setupKeyProtocolTest(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-a-1111", KeyProtocol: Chat}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "B", "sk-b-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "C", "sk-c-3333", ""); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(file{Providers: []Provider{mustFind(t, "relay")}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"keyProtocol":"chat"`) || !strings.Contains(s, `"protocol":"anthropic"`) {
		t.Fatalf("protocols not in JSON: %s", s)
	}
	if strings.Count(s, `"protocol":`) != 1 {
		t.Fatalf("an unlimited key should omit protocol: %s", s)
	}
	p := mustFind(t, "relay")
	if p.KeyProtocol != Chat || p.Keys[0].Protocol != Anthropic || p.Keys[1].Protocol != "" {
		t.Fatalf("read back: %q %+v", p.KeyProtocol, p.Keys)
	}
}

func mustFind(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestWithKeyFiltersEndpoints(t *testing.T) {
	base := Provider{
		ID: "relay", Key: "sk-first",
		Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test",
	}

	chatOnly := base.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chatOnly.Key != "sk-chat" || chatOnly.KeyProtocol != Chat {
		t.Fatalf("key not replaced: %+v", chatOnly)
	}
	if chatOnly.Chat != base.Chat || chatOnly.Responses != "" || chatOnly.Anthropic != "" {
		t.Fatalf("chat-limited key kept other endpoints: %+v", chatOnly)
	}

	antOnly := base.WithKey(KeyAccount{Key: "sk-ant", Protocol: Anthropic})
	if antOnly.Anthropic != base.Anthropic || antOnly.Chat != "" || antOnly.Responses != "" {
		t.Fatalf("anthropic-limited key endpoints: %+v", antOnly)
	}

	all := base.WithKey(KeyAccount{Key: "sk-any"})
	if all.Chat != base.Chat || all.Responses != base.Responses || all.Anthropic != base.Anthropic {
		t.Fatalf("unlimited key lost endpoints: %+v", all)
	}
	if all.KeyProtocol != "" {
		t.Fatalf("unlimited key kept a protocol: %q", all.KeyProtocol)
	}

	// the original is untouched
	if base.Key != "sk-first" || base.Chat == "" || base.Responses == "" || base.Anthropic == "" {
		t.Fatalf("WithKey changed the original: %+v", base)
	}
}
