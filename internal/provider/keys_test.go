package provider

import (
	"reflect"
	"testing"
)

// keysSetup saves one provider with one key, "one", in use.
func keysSetup(t *testing.T) {
	t.Helper()
	isolateConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-one", KeyName: "one"}); err != nil {
		t.Fatal(err)
	}
}

// keyState reads the provider back: the names and enabled flags of its keys,
// in order, the key in use first.
func keyState(t *testing.T) (names []string, on []bool) {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range p.AllKeys() {
		names = append(names, k.Name)
		on = append(on, !k.Off)
	}
	return names, on
}

func TestKeyRefIsTenHexDigits(t *testing.T) {
	ref := KeyRef("sk-one")
	if len(ref) != 10 || ref != KeyRef("sk-one") || ref == KeyRef("sk-two") {
		t.Fatalf("KeyRef = %q", ref)
	}
	for _, c := range ref {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			t.Fatalf("KeyRef %q is not lowercase hex", ref)
		}
	}
}

func TestAddKeyAppendsAndRejectsBadInput(t *testing.T) {
	keysSetup(t)
	if err := AddKey("relay", "two", "sk-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "dup", "sk-two", Chat); err == nil {
		t.Error("a repeated key was accepted")
	}
	if err := AddKey("relay", "dup", "sk-one", ""); err == nil {
		t.Error("a repeat of the key in use was accepted")
	}
	if err := AddKey("relay", "x", "sk-x", Gemini); err == nil {
		t.Error("gemini accepted as a key protocol")
	}
	if err := AddKey("relay", "x", "sk-x", Protocol("bogus")); err == nil {
		t.Error("unknown protocol accepted")
	}
	if err := AddKey("nope", "x", "sk-x", ""); err == nil {
		t.Error("unknown provider accepted")
	}
	names, on := keyState(t)
	if !reflect.DeepEqual(names, []string{"one", "two"}) || !reflect.DeepEqual(on, []bool{true, true}) {
		t.Fatalf("keys = %q enabled %v", names, on)
	}
}

func TestAddKeyToKeylessProviderMakesItInUse(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "first", "sk-first", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("local")
	if p.Key != "sk-first" || p.KeyName != "first" || p.KeyProtocol != Anthropic || len(p.Keys) != 0 {
		t.Fatalf("provider = %+v", p)
	}
}

func TestLastEnabledKeyCannotBeDisabledOrRemoved(t *testing.T) {
	keysSetup(t)
	ref := KeyRef("sk-one")
	if err := SetKeyOn("relay", ref, false); err == nil {
		t.Error("the last enabled key was disabled")
	}
	if err := RemoveKey("relay", ref); err == nil {
		t.Error("the last enabled key was removed")
	}
	if names, on := keyState(t); !reflect.DeepEqual(names, []string{"one"}) || !on[0] {
		t.Fatalf("refused change still changed keys: %q %v", names, on)
	}

	if err := AddKey("relay", "two", "sk-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-two"), false); err != nil {
		t.Fatal(err)
	}
	// "one" is now the only enabled key
	if err := RemoveKey("relay", ref); err == nil {
		t.Error("removing the last enabled key was allowed")
	}
	if err := SetKeyOn("relay", ref, false); err == nil {
		t.Error("disabling the last enabled key was allowed")
	}
	if names, on := keyState(t); !reflect.DeepEqual(names, []string{"one", "two"}) || !reflect.DeepEqual(on, []bool{true, false}) {
		t.Fatalf("keys = %q enabled %v", names, on)
	}
	// a disabled key may go
	if err := RemoveKey("relay", KeyRef("sk-two")); err != nil {
		t.Fatal(err)
	}
	if names, _ := keyState(t); !reflect.DeepEqual(names, []string{"one"}) {
		t.Fatalf("keys = %q", names)
	}
}

func TestDisablingTheKeyInUseHandsOver(t *testing.T) {
	keysSetup(t)
	if err := AddKey("relay", "two", "sk-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-one"), false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-two" || p.KeyName != "two" {
		t.Fatalf("in use = %q/%q, want the second key", p.Key, p.KeyName)
	}
	if names, on := keyState(t); !reflect.DeepEqual(names, []string{"two", "one"}) || !reflect.DeepEqual(on, []bool{true, false}) {
		t.Fatalf("keys = %q enabled %v", names, on)
	}
	// nothing else is enabled now, so the change is refused and nothing moves
	if err := SetKeyOn("relay", KeyRef("sk-two"), false); err == nil {
		t.Fatal("disabling the only enabled key was allowed")
	}
	if p, _ := Find("relay"); p.Key != "sk-two" {
		t.Fatalf("refused change moved the key in use to %q", p.Key)
	}
	// and it can be turned back on
	if err := SetKeyOn("relay", KeyRef("sk-one"), true); err != nil {
		t.Fatal(err)
	}
	if names, on := keyState(t); !reflect.DeepEqual(names, []string{"two", "one"}) || !reflect.DeepEqual(on, []bool{true, true}) {
		t.Fatalf("keys = %q enabled %v", names, on)
	}
}

func TestUseKeyKeepsOtherKeysAndProtocols(t *testing.T) {
	keysSetup(t)
	if err := AddKey("relay", "two", "sk-two", Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "three", "sk-three", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("sk-three")); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if p.Key != "sk-three" || p.KeyName != "three" || p.KeyProtocol != Anthropic {
		t.Fatalf("in use = %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	want := []KeyEntry{
		{Name: "one", Key: "sk-one"},
		{Name: "two", Key: "sk-two", Protocol: Chat},
	}
	if !reflect.DeepEqual(p.Keys, want) {
		t.Fatalf("other keys = %+v, want %+v", p.Keys, want)
	}
	// the previous key in use stays enabled, right behind the new one
	if names, on := keyState(t); !reflect.DeepEqual(names, []string{"three", "one", "two"}) || !reflect.DeepEqual(on, []bool{true, true, true}) {
		t.Fatalf("keys = %q enabled %v", names, on)
	}
}

func TestUseKeyTurnsOnADisabledKey(t *testing.T) {
	keysSetup(t)
	if err := AddKey("relay", "two", "sk-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-two"), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("sk-two")); err != nil {
		t.Fatal(err)
	}
	if names, on := keyState(t); !reflect.DeepEqual(names, []string{"two", "one"}) || !reflect.DeepEqual(on, []bool{true, true}) {
		t.Fatalf("keys = %q enabled %v", names, on)
	}
}

func TestSetKeyProtocolAndRename(t *testing.T) {
	keysSetup(t)
	if err := AddKey("relay", "two", "sk-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-two"), Responses); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-two"), Protocol("gemini")); err == nil {
		t.Fatal("gemini accepted as a key protocol")
	}
	if err := RenameKey("relay", KeyRef("sk-two"), "team"); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if got := p.AllKeys()[1]; got.Name != "team" || got.Protocol != Responses || got.Key != "sk-two" {
		t.Fatalf("second key = %+v", got)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-two"), ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); p.AllKeys()[1].Protocol != "" {
		t.Fatal("protocol limit was not cleared")
	}
	if err := RemoveKey("relay", "0000000000"); err == nil {
		t.Fatal("unknown key ref accepted")
	}
}

func TestKeyForRespectsProtocolLimits(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://relay.example/v1", Anthropic: "https://relay.example",
		Key: "sk-oai", KeyProtocol: Chat,
		Keys: []KeyEntry{{Name: "anth", Key: "sk-anth", Protocol: Anthropic}}}
	if kp, ok := p.KeyFor(Anthropic); !ok || kp.Key != "sk-anth" {
		t.Fatalf("anthropic key = %q %v, want sk-anth", kp.Key, ok)
	}
	if kp, ok := p.KeyFor(Chat); !ok || kp.Key != "sk-oai" {
		t.Fatalf("chat key = %q %v, want sk-oai", kp.Key, ok)
	}
	onlyChat := Provider{ID: "c", Chat: "https://c.example/v1", Key: "sk-oai", KeyProtocol: Chat}
	if _, ok := onlyChat.KeyFor(Anthropic); ok {
		t.Fatal("a chat-only key was offered for anthropic")
	}
}
