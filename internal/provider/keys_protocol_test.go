package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func useTempConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestAddKeyProtocol(t *testing.T) {
	useTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Bad", "sk-bad-3333", "carrier-pigeon"); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if p, _ := Find("relay"); len(p.KeyList()) != 1 || len(p.Keys) != 0 {
		t.Fatalf("a rejected key was saved: %+v", p.Keys)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || ks[1].Protocol != Anthropic || ks[0].Protocol != "" {
		t.Fatalf("protocols after add: %+v", ks)
	}
}

func TestSetKeyProtocolFirstKey(t *testing.T) {
	useTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), "CHAT"); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if ks := p.KeyList(); ks[0].Protocol != Chat {
		t.Fatalf("first key protocol %q, want chat", ks[0].Protocol)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), "gemini"); err == nil {
		t.Fatal("gemini accepted as a key protocol")
	}
	if err := SetKeyProtocol("relay", keyID("sk-missing"), Chat); err == nil {
		t.Fatal("a missing key was found")
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), ""); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); p.KeyProtocol != "" {
		t.Fatalf("protocol not cleared: %q", p.KeyProtocol)
	}
}

func TestProtocolFollowsKeyOnSwitch(t *testing.T) {
	useTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", keyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-ant-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("new first key lost its limit: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-chat-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("old first key lost its limit: %+v", p.Keys)
	}
	// turning the first key off: the next one on takes the lead with its own limit
	if err := SetKeyOn("relay", keyID("sk-ant-2222"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-chat-1111" || p.KeyProtocol != Chat {
		t.Fatalf("after off, lead %q limited to %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-ant-2222" || p.Keys[0].Protocol != Anthropic || !p.Keys[0].Off {
		t.Fatalf("after off, saved %+v", p.Keys)
	}
	// removing the saved key takes its limit with it
	if err := RemoveKey("relay", keyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if len(p.Keys) != 0 || p.KeyProtocol != Chat {
		t.Fatalf("after remove: %+v limit %q", p.Keys, p.KeyProtocol)
	}
}

func TestWithKeyEndpoints(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test"}

	chat := p.WithKey(KeyAccount{Key: "sk-chat", Protocol: Chat})
	if chat.Chat != p.Chat || chat.Responses != "" || chat.Anthropic != "" || chat.Key != "sk-chat" {
		t.Fatalf("chat-limited copy: %+v", chat)
	}
	if p.Responses == "" || p.Anthropic == "" || p.Key != "" {
		t.Fatalf("WithKey changed the receiver: %+v", p)
	}

	all := p.WithKey(KeyAccount{Key: "sk-any"})
	if all.Chat != p.Chat || all.Responses != p.Responses || all.Anthropic != p.Anthropic || all.Key != "sk-any" {
		t.Fatalf("unlimited copy lost endpoints: %+v", all)
	}
}

func TestProtocolJSON(t *testing.T) {
	limited := Provider{ID: "relay", KeyProtocol: Chat, Keys: []KeyAccount{{Key: "sk-a", Protocol: Anthropic}}}
	b, err := json.Marshal(limited)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, `"keyProtocol":"chat"`) || !strings.Contains(s, `"protocol":"anthropic"`) {
		t.Fatalf("limits not serialized: %s", s)
	}
	plain := Provider{ID: "relay", Key: "sk-a", Keys: []KeyAccount{{Key: "sk-b"}}}
	b, _ = json.Marshal(plain)
	if s := string(b); strings.Contains(s, "keyProtocol") || strings.Contains(s, `"protocol"`) {
		t.Fatalf("empty limits not omitted: %s", s)
	}
	b, _ = json.Marshal(KeyInfo{ID: "x"})
	if strings.Contains(string(b), "protocol") {
		t.Fatalf("empty KeyInfo protocol not omitted: %s", b)
	}
	b, _ = json.Marshal(KeyInfo{ID: "x", Protocol: Responses})
	if !strings.Contains(string(b), `"protocol":"responses"`) {
		t.Fatalf("KeyInfo protocol not serialized: %s", b)
	}
}
