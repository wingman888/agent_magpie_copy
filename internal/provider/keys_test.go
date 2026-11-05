package provider

import (
	"encoding/hex"
	"strings"
	"testing"
)

// relayWithKeys saves a custom relay whose current key is sk-1 ("one").
func relayWithKeys(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1",
		Anthropic: "https://relay.example", Key: "sk-1", KeyName: "one"}); err != nil {
		t.Fatal(err)
	}
}

func storedRelay(t *testing.T) Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestKeyRefIsTenLowercaseHex(t *testing.T) {
	r := KeyRef("sk-1")
	if r != "0f2c10bf3d" {
		t.Fatalf("KeyRef(sk-1) = %q, want the first 5 bytes of its SHA-256", r)
	}
	if len(r) != 10 || strings.ToLower(r) != r {
		t.Fatalf("KeyRef = %q, want 10 lowercase hex digits", r)
	}
	if _, err := hex.DecodeString(r); err != nil {
		t.Fatal(err)
	}
	if KeyRef("sk-2") == r {
		t.Fatal("different keys share a fingerprint")
	}
}

func TestAddKeyRules(t *testing.T) {
	relayWithKeys(t)
	if err := AddKey("relay", "dup", "sk-1", ""); err == nil {
		t.Fatal("a key already on the provider was accepted")
	}
	if err := AddKey("relay", "bad", "sk-9", Protocol("gemini")); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	if err := AddKey("relay", "team", "sk-2", Anthropic); err != nil {
		t.Fatal(err)
	}
	p := storedRelay(t)
	if p.Key != "sk-1" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-2" || p.Keys[0].Off ||
		p.Keys[0].Name != "team" || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("after AddKey: %+v", p)
	}
}

func TestFirstAddedKeyBecomesCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "mine", "sk-l", ""); err != nil {
		t.Fatal(err)
	}
	p := storedRelayID(t, "local")
	if p.Key != "sk-l" || p.KeyName != "mine" || len(p.Keys) != 0 {
		t.Fatalf("first key: %+v", p)
	}
}

func storedRelayID(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestLastEnabledKeyCannotGoOffOrBeDeleted(t *testing.T) {
	relayWithKeys(t)
	if err := AddKey("relay", "team", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-2"), false); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-1"), false); err == nil {
		t.Fatal("the last enabled key was turned off")
	}
	if err := RemoveKey("relay", KeyRef("sk-1")); err == nil {
		t.Fatal("the last enabled key was deleted")
	}
	if p := storedRelay(t); p.Key != "sk-1" {
		t.Fatalf("the current key changed: %+v", p)
	}
	if err := RemoveKey("relay", KeyRef("sk-2")); err != nil {
		t.Fatalf("removing a disabled key: %v", err)
	}
}

func TestDisablingCurrentKeyHandsOver(t *testing.T) {
	relayWithKeys(t)
	AddKey("relay", "two", "sk-2", "")
	AddKey("relay", "three", "sk-3", "")
	if err := SetKeyOn("relay", KeyRef("sk-1"), false); err != nil {
		t.Fatal(err)
	}
	p := storedRelay(t)
	if p.Key != "sk-2" || p.KeyName != "two" {
		t.Fatalf("next enabled key did not take over: %+v", p)
	}
	if len(p.Keys) != 2 || p.Keys[0].Key != "sk-1" || !p.Keys[0].Off || p.Keys[0].Name != "one" || p.Keys[1].Key != "sk-3" {
		t.Fatalf("disabled key not kept: %+v", p.Keys)
	}
	if err := SetKeyOn("relay", KeyRef("sk-1"), true); err != nil {
		t.Fatal(err)
	}
	if p := storedRelay(t); p.Keys[0].Off {
		t.Fatal("re-enabling a key did not stick")
	}
}

func TestRemovingCurrentKeyHandsOver(t *testing.T) {
	relayWithKeys(t)
	AddKey("relay", "two", "sk-2", Chat)
	if err := RemoveKey("relay", KeyRef("sk-1")); err != nil {
		t.Fatal(err)
	}
	p := storedRelay(t)
	if p.Key != "sk-2" || p.KeyProtocol != Chat || len(p.Keys) != 0 {
		t.Fatalf("after removing the current key: %+v", p)
	}
}

func TestUseKeyKeepsOtherKeysAndProtocols(t *testing.T) {
	relayWithKeys(t)
	AddKey("relay", "two", "sk-2", Anthropic)
	AddKey("relay", "three", "sk-3", Chat)
	if err := UseKey("relay", KeyRef("sk-3")); err != nil {
		t.Fatal(err)
	}
	p := storedRelay(t)
	if p.Key != "sk-3" || p.KeyName != "three" || p.KeyProtocol != Chat {
		t.Fatalf("new current key: %+v", p)
	}
	want := []KeyEntry{
		{Name: "one", Key: "sk-1"},
		{Name: "two", Key: "sk-2", Protocol: Anthropic},
	}
	if len(p.Keys) != len(want) {
		t.Fatalf("keys after UseKey: %+v", p.Keys)
	}
	for i, w := range want {
		if p.Keys[i].Name != w.Name || p.Keys[i].Key != w.Key || p.Keys[i].Protocol != w.Protocol || p.Keys[i].Off {
			t.Fatalf("key %d after UseKey: %+v, want %+v", i, p.Keys[i], w)
		}
	}
}

func TestRenameAndSetProtocol(t *testing.T) {
	relayWithKeys(t)
	AddKey("relay", "two", "sk-2", "")
	if err := RenameKey("relay", KeyRef("sk-2"), "team"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-2"), Responses); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-1"), Protocol("gemini")); err == nil {
		t.Fatal("an unknown protocol was accepted")
	}
	p := storedRelay(t)
	if p.Keys[0].Name != "team" || p.Keys[0].Protocol != Responses {
		t.Fatalf("after rename/protocol: %+v", p.Keys[0])
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-1"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if p := storedRelay(t); p.KeyProtocol != Anthropic {
		t.Fatalf("current key protocol: %q", p.KeyProtocol)
	}
}

func TestSaveRejectsUnknownKeyProtocol(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	err := Save(Provider{ID: "bad", Name: "Bad", Chat: "https://x.example/v1", Key: "sk",
		Keys: []KeyEntry{{Key: "sk-2", Protocol: "gemini"}}})
	if err == nil {
		t.Fatal("Save accepted an unknown key protocol")
	}
}

func TestChoicesOrderByModelFamily(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://r.example/v1", Anthropic: "https://r.example",
		Key: "sk-c", KeyProtocol: Chat,
		Keys: []KeyEntry{
			{Name: "a", Key: "sk-a", Protocol: Anthropic},
			{Key: "sk-off", Protocol: Anthropic, Off: true},
			{Key: "sk-any"},
		}}
	keys := func(cs []Choice) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Provider.Key)
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if got := keys(p.Choices(Anthropic, "claude-sonnet-5-5")); !eq(got, []string{"sk-a", "sk-any", "sk-c"}) {
		t.Fatalf("claude model: %v", got)
	}
	if got := keys(p.Choices(Chat, "gpt-5.5")); !eq(got, []string{"sk-c", "sk-any", "sk-a"}) {
		t.Fatalf("gpt model: %v", got)
	}
	if got := keys(p.Choices(Chat, "deepseek/deepseek-chat")); !eq(got, []string{"sk-c", "sk-any", "sk-a"}) {
		t.Fatalf("other model from chat: %v", got)
	}
	// a key limited to Anthropic reaches only the Anthropic endpoint
	for _, c := range p.Choices(Chat, "deepseek-chat") {
		if c.Provider.Key == "sk-a" && (c.Provider.Chat != "" || c.Provider.Responses != "" || c.Provider.Anthropic == "") {
			t.Fatalf("anthropic-only key sees other endpoints: %+v", c.Provider)
		}
	}
}

func TestChoicesSkipKeysWithoutEndpoint(t *testing.T) {
	p := Provider{ID: "chatonly", Name: "Chat only", Chat: "https://c.example/v1", Key: "sk-1", KeyProtocol: Anthropic}
	if got := p.Choices(Chat, "gpt-5.5"); len(got) != 0 {
		t.Fatalf("a key for a missing endpoint was offered: %d choices", len(got))
	}
}
