package provider

// Acceptance tests for "an API key may be limited to one protocol",
// items 6–8, checked through the exported API only.

import (
	"encoding/json"
	"strings"
	"testing"
)

// 验收 6: 添加 Key 时给出未知协议会失败且不保存；合法协议保存后 KeyList 中可见。
func TestAcceptance6AddKeyWithProtocol(t *testing.T) {
	useTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}

	if err := AddKey("relay", "Pigeon", "sk-pigeon-3333", "carrier-pigeon"); err == nil {
		t.Fatal("AddKey accepted the unknown protocol carrier-pigeon")
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Keys) != 0 || len(p.KeyList()) != 1 {
		t.Fatalf("a key with an unknown protocol was saved: %+v", p.KeyList())
	}
	for _, k := range p.KeyList() {
		if k.ID == KeyID("sk-pigeon-3333") {
			t.Fatal("the rejected key shows up in the key list")
		}
	}

	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatalf("AddKey with a valid protocol: %v", err)
	}
	p, err = Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ks := p.KeyList()
	if len(ks) != 2 {
		t.Fatalf("key list %+v, want 2 keys", ks)
	}
	if ks[1].ID != KeyID("sk-ant-2222") || ks[1].Protocol != Anthropic || ks[1].Name != "Claude" {
		t.Fatalf("added key in list: %+v", ks[1])
	}
	if ks[0].Protocol != "" {
		t.Fatalf("first key picked up a protocol it was never given: %q", ks[0].Protocol)
	}

	// the list output carries protocol for limited keys and omits it otherwise
	b, err := json.Marshal(ks)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"protocol":"anthropic"`) {
		t.Fatalf("key list JSON has no protocol for the limited key: %s", s)
	}
	if strings.Count(s, `"protocol"`) != 1 {
		t.Fatalf("key list JSON protocol count wrong (empty should be omitted): %s", s)
	}
}

// 验收 7: SetKeyProtocol 对第一把生效；切换当前 Key 后限定跟随各自的 Key。
func TestAcceptance7KeyProtocolFollowsKeyOnSwitch(t *testing.T) {
	useTempConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Anthropic: "https://relay.test", Key: "sk-chat-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Claude", "sk-ant-2222", Anthropic); err != nil {
		t.Fatal(err)
	}

	if err := SetKeyProtocol("relay", KeyID("sk-chat-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if ks[0].ID != KeyID("sk-chat-1111") || ks[0].Protocol != Chat || !ks[0].Active {
		t.Fatalf("first key after SetKeyProtocol: %+v", ks[0])
	}

	// make the Anthropic key current: the chat-limited key goes behind it, still chat
	if err := UseKey("relay", KeyID("sk-ant-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	ks = p.KeyList()
	if len(ks) != 2 {
		t.Fatalf("key list %+v", ks)
	}
	if ks[0].ID != KeyID("sk-ant-2222") || ks[0].Protocol != Anthropic || !ks[0].Active {
		t.Fatalf("new current key lost its limit: %+v", ks[0])
	}
	if ks[1].ID != KeyID("sk-chat-1111") || ks[1].Protocol != Chat {
		t.Fatalf("old current key's limit did not follow it: %+v", ks[1])
	}
	if p.Key != "sk-ant-2222" || p.KeyProtocol != Anthropic {
		t.Fatalf("stored first key %q limited to %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 1 || p.Keys[0].Key != "sk-chat-1111" || p.Keys[0].Protocol != Chat {
		t.Fatalf("stored saved key %+v", p.Keys)
	}

	// changing the limit of the key now behind it does not touch the current one
	if err := SetKeyProtocol("relay", KeyID("sk-chat-1111"), Responses); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.KeyProtocol != Anthropic || p.Keys[0].Protocol != Responses {
		t.Fatalf("limits after changing the saved key: first %q, saved %q", p.KeyProtocol, p.Keys[0].Protocol)
	}
}

// 验收 8: WithKey 按 Key 的协议限定保留端点；无限定则保留全部端点。
func TestAcceptance8WithKeyKeepsOnlyTheLimitedEndpoint(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Responses: "https://relay.test/r/v1", Anthropic: "https://relay.test"}

	chatOnly := p.WithKey(KeyAccount{Name: "GPT", Key: "sk-chat", Protocol: Chat})
	if chatOnly.Chat != p.Chat || chatOnly.Responses != "" || chatOnly.Anthropic != "" {
		t.Fatalf("chat-limited key kept endpoints chat=%q responses=%q anthropic=%q", chatOnly.Chat, chatOnly.Responses, chatOnly.Anthropic)
	}
	if chatOnly.Key != "sk-chat" || chatOnly.KeyName != "GPT" {
		t.Fatalf("chat-limited copy uses key %q name %q", chatOnly.Key, chatOnly.KeyName)
	}

	respOnly := p.WithKey(KeyAccount{Key: "sk-resp", Protocol: Responses})
	if respOnly.Chat != "" || respOnly.Responses != p.Responses || respOnly.Anthropic != "" {
		t.Fatalf("responses-limited key kept endpoints %+v", respOnly)
	}

	antOnly := p.WithKey(KeyAccount{Key: "sk-ant", Protocol: Anthropic})
	if antOnly.Chat != "" || antOnly.Responses != "" || antOnly.Anthropic != p.Anthropic {
		t.Fatalf("anthropic-limited key kept endpoints %+v", antOnly)
	}

	any := p.WithKey(KeyAccount{Key: "sk-any"})
	if any.Chat != p.Chat || any.Responses != p.Responses || any.Anthropic != p.Anthropic || any.Key != "sk-any" {
		t.Fatalf("unlimited key lost endpoints: %+v", any)
	}

	// the receiver is unchanged by any of the above
	if p.Chat == "" || p.Responses == "" || p.Anthropic == "" || p.Key != "" {
		t.Fatalf("WithKey changed its receiver: %+v", p)
	}
}
