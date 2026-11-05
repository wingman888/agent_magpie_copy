package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

// relayWithEndpoints saves a provider with a Chat and an Anthropic endpoint
// and a first key, for the protocol-limit tests.
func relayWithEndpoints(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
}

func TestAddKeyUnknownProtocolRefused(t *testing.T) {
	relayWithEndpoints(t)
	if err := AddKey("relay", "Bad", "sk-bad-3333", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	p, _ := Find("relay")
	if len(p.KeyList()) != 1 || len(p.Keys) != 0 {
		t.Fatalf("a refused key was saved: %+v", p.Keys)
	}
}

func TestAddKeyProtocolSaved(t *testing.T) {
	relayWithEndpoints(t)
	if err := AddKey("relay", "Claude", "sk-claude-4444", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic || ks[1].Name != "Claude" {
		t.Fatalf("key list %+v", ks)
	}
	if ks[0].Protocol != "" {
		t.Fatalf("the first key got a protocol: %+v", ks[0])
	}
}

func TestSetKeyProtocolFollowsKey(t *testing.T) {
	relayWithEndpoints(t)
	if err := AddKey("relay", "Team", "sk-team-2222", ""); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	first, team := keyID("sk-personal-1111"), keyID("sk-team-2222")

	if err := SetKeyProtocol("relay", first, Chat); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", team, Anthropic); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.KeyProtocol != Chat || len(p.Keys) != 1 || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("after set: %q %+v", p.KeyProtocol, p.Keys)
	}
	if ks := p.KeyList(); ks[0].Protocol != Chat || ks[1].Protocol != Anthropic {
		t.Fatalf("key list after set: %+v", ks)
	}

	// switching the current key moves each limit with its key
	if err := UseKey("relay", team); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-team-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("new current key lost its limit: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-personal-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("old current key lost its limit: %+v", p.Keys)
	}

	// and back again
	if err := UseKey("relay", first); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-personal-1111" || p.KeyProtocol != Chat || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("after switching back: %q %+v", p.KeyProtocol, p.Keys)
	}

	// lifting the limit clears it, and only that key
	if err := SetKeyProtocol("relay", first, ""); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.KeyProtocol != "" || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("after clearing: %q %+v", p.KeyProtocol, p.Keys)
	}
}

func TestSetKeyProtocolRefusals(t *testing.T) {
	relayWithEndpoints(t)
	if err := SetKeyProtocol("relay", keyID("sk-personal-1111"), "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if err := SetKeyProtocol("relay", "ffffffffff", Chat); err == nil {
		t.Fatal("a key that does not exist was found")
	}
	if p, _ := Find("relay"); p.KeyProtocol != "" {
		t.Fatalf("a refused change was saved: %q", p.KeyProtocol)
	}
}

func TestPromoteCarriesProtocol(t *testing.T) {
	relayWithEndpoints(t)
	if err := AddKey("relay", "Claude", "sk-claude-4444", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-personal-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	// turning the first key off hands the lead to the next key on
	if err := SetKeyOn("relay", keyID("sk-personal-1111"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-claude-4444" || p.KeyProtocol != Anthropic {
		t.Fatalf("new lead: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-personal-1111" || p.Keys[0].Protocol != Chat || !p.Keys[0].Off {
		t.Fatalf("turned-off key: %+v", p.Keys)
	}
	// removing the lead hands over its limit too
	if err := SetKeyOn("relay", keyID("sk-personal-1111"), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID("sk-claude-4444")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-personal-1111" || p.KeyProtocol != Chat || len(p.Keys) != 0 {
		t.Fatalf("after remove: %q %q %+v", p.Key, p.KeyProtocol, p.Keys)
	}
}

func TestWithKeyLimitsEndpoints(t *testing.T) {
	relayWithEndpoints(t)
	p, _ := Find("relay")
	p.Headers = map[string]string{"X-Test": "1"}
	p.Fallback = []string{"relay/other"}

	chatOnly := p.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chatOnly.Chat != p.Chat || chatOnly.Anthropic != "" || chatOnly.Responses != "" {
		t.Fatalf("chat-limited key kept %q %q %q", chatOnly.Chat, chatOnly.Responses, chatOnly.Anthropic)
	}
	if chatOnly.Key != "sk-chat" || chatOnly.KeyProtocol != Chat {
		t.Fatalf("chat-limited key not in use: %q %q", chatOnly.Key, chatOnly.KeyProtocol)
	}
	// the original is untouched
	if p.Anthropic == "" || p.Chat == "" || p.Key != "sk-personal-1111" {
		t.Fatalf("WithKey changed the original: %+v", p)
	}

	all := p.WithKey(KeyAccount{Key: "sk-any"})
	if all.Chat != p.Chat || all.Anthropic != p.Anthropic || all.Responses != p.Responses {
		t.Fatalf("unlimited key lost endpoints: %+v", all)
	}

	// maps and slices are copies, not shared with the original
	all.Headers["X-Test"] = "2"
	all.Fallback[0] = "changed"
	if p.Headers["X-Test"] != "1" || p.Fallback[0] != "relay/other" {
		t.Fatalf("WithKey shares memory with the original: %+v %+v", p.Headers, p.Fallback)
	}
}

func TestKeyProtocolJSON(t *testing.T) {
	b, _ := json.Marshal(KeyAccount{Key: "sk-x"})
	if strings.Contains(string(b), "protocol") {
		t.Fatalf("empty protocol written: %s", b)
	}
	b, _ = json.Marshal(KeyAccount{Key: "sk-x", Protocol: Chat})
	if !strings.Contains(string(b), `"protocol":"chat"`) {
		t.Fatalf("protocol not written: %s", b)
	}
	b, _ = json.Marshal(KeyInfo{ID: "abc", Masked: "x"})
	if strings.Contains(string(b), "protocol") {
		t.Fatalf("empty protocol written in KeyInfo: %s", b)
	}
	b, _ = json.Marshal(KeyInfo{ID: "abc", Masked: "x", Protocol: Anthropic})
	if !strings.Contains(string(b), `"protocol":"anthropic"`) {
		t.Fatalf("protocol not written in KeyInfo: %s", b)
	}
	b, _ = json.Marshal(Provider{ID: "relay", Key: "sk-x"})
	if strings.Contains(string(b), "keyProtocol") {
		t.Fatalf("empty keyProtocol written: %s", b)
	}
	b, _ = json.Marshal(Provider{ID: "relay", Key: "sk-x", KeyProtocol: Responses})
	if !strings.Contains(string(b), `"keyProtocol":"responses"`) {
		t.Fatalf("keyProtocol not written: %s", b)
	}
}
