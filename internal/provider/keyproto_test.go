package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKeyProtocolLimitsFollowKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relayp", Name: "RelayP", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-first-aaaa"}); err != nil {
		t.Fatal(err)
	}

	// an unknown protocol is refused and nothing is saved
	if err := AddKey("relayp", "Bad", "sk-bad-bbbb", "carrier-pigeon"); err == nil {
		t.Fatal("unknown protocol was accepted")
	}
	if p, _ := Find("relayp"); len(p.Keys) != 0 {
		t.Fatalf("a refused key was saved: %+v", p.Keys)
	}

	// a known protocol is saved and listed
	if err := AddKey("relayp", "Claude", "sk-claude-cccc", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relayp")
	ks := p.KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic || ks[0].Protocol != "" {
		t.Fatalf("after add: %+v", ks)
	}
	claudeID := ks[1].ID
	firstID := ks[0].ID

	// unknown protocol or missing key in SetKeyProtocol fails
	if err := SetKeyProtocol("relayp", firstID, "carrier-pigeon"); err == nil {
		t.Fatal("SetKeyProtocol accepted an unknown protocol")
	}
	if err := SetKeyProtocol("relayp", "0000000000", Chat); err == nil {
		t.Fatal("SetKeyProtocol accepted a missing key")
	}

	// limit the first key to chat; it shows first in the list
	if err := SetKeyProtocol("relayp", firstID, Chat); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relayp")
	if ks = p.KeyList(); ks[0].Protocol != Chat {
		t.Fatalf("first key protocol: %+v", ks)
	}

	// switch the current key: the limits move with their keys
	if err := UseKey("relayp", claudeID); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relayp")
	ks = p.KeyList()
	if ks[0].ID != claudeID || ks[0].Protocol != Anthropic {
		t.Fatalf("new first key lost its limit: %+v", ks)
	}
	if ks[1].ID != firstID || ks[1].Protocol != Chat {
		t.Fatalf("old first key lost its limit: %+v", ks)
	}

	// turning the current key off hands the lead on; limits still follow
	if err := SetKeyOn("relayp", claudeID, false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relayp")
	ks = p.KeyList()
	if ks[0].ID != firstID || ks[0].Protocol != Chat {
		t.Fatalf("after off, lead lost its limit: %+v", ks)
	}
	if ks[1].ID != claudeID || ks[1].Protocol != Anthropic {
		t.Fatalf("after off, parked key lost its limit: %+v", ks)
	}

	// clear the limit again
	if err := SetKeyProtocol("relayp", firstID, ""); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relayp")
	if ks = p.KeyList(); ks[0].Protocol != "" {
		t.Fatalf("clearing the limit failed: %+v", ks)
	}

	// removing the current key hands the lead to the next one, with its limit
	if err := RemoveKey("relayp", firstID); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relayp")
	ks = p.KeyList()
	if len(ks) != 1 || ks[0].ID != claudeID || ks[0].Protocol != Anthropic {
		t.Fatalf("after remove: %+v", ks)
	}
}

func TestKeyProtocolWithKeyScopesEndpoints(t *testing.T) {
	base := Provider{ID: "scopep", Name: "ScopeP", Key: "sk-one", Chat: "https://c.test/v1", Responses: "https://r.test/v1", Anthropic: "https://a.test"}

	chat := base.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chat.Key != "sk-chat" || chat.Chat == "" || chat.Responses != "" || chat.Anthropic != "" {
		t.Fatalf("chat-limited key: %+v", chat)
	}

	open := base.WithKey(KeyAccount{Key: "sk-any"})
	if open.Key != "sk-any" || open.Chat == "" || open.Responses == "" || open.Anthropic == "" {
		t.Fatalf("unlimited key should keep every endpoint: %+v", open)
	}

	// the original provider is untouched
	if base.Key != "sk-one" || base.Chat == "" || base.Responses == "" || base.Anthropic == "" {
		t.Fatalf("WithKey changed the receiver: %+v", base)
	}
}

func TestKeyProtocolJSONKeys(t *testing.T) {
	b, err := json.Marshal(KeyAccount{Key: "sk-x", Protocol: Anthropic})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"protocol":"anthropic"`) {
		t.Fatalf("KeyAccount JSON: %s", b)
	}
	b, _ = json.Marshal(KeyAccount{Key: "sk-x"})
	if strings.Contains(string(b), "protocol") {
		t.Fatalf("empty protocol should be omitted: %s", b)
	}
	b, _ = json.Marshal(Provider{ID: "y", KeyProtocol: Chat})
	if !strings.Contains(string(b), `"keyProtocol":"chat"`) {
		t.Fatalf("Provider JSON: %s", b)
	}
	b, _ = json.Marshal(KeyInfo{ID: "1"})
	if strings.Contains(string(b), "protocol") {
		t.Fatalf("empty KeyInfo protocol should be omitted: %s", b)
	}
}
