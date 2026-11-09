package provider

import (
	"path/filepath"
	"strings"
	"testing"
)

// s2kSandbox points HOME and the config at a fresh temp dir.
func s2kSandbox(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// s2kRelay saves a provider speaking chat and anthropic, with one key.
func s2kRelay(t *testing.T, key string) {
	t.Helper()
	if err := Save(Provider{ID: "s2krelay", Name: "relay", Key: key,
		Chat: "https://relay.example/v1", Anthropic: "https://relay.example"}); err != nil {
		t.Fatal(err)
	}
}

func s2kFind(t *testing.T) Provider {
	t.Helper()
	p, err := Find("s2krelay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestS2KAddKeyRules(t *testing.T) {
	s2kSandbox(t)
	s2kRelay(t, "k1")
	if err := AddKey("s2krelay", "", "k1", ""); err == nil {
		t.Error("a duplicate key was accepted")
	}
	if err := AddKey("s2krelay", "x", "k2", "gemini"); err == nil {
		t.Error("an unknown protocol was accepted")
	}
	if err := AddKey("s2krelay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	p := s2kFind(t)
	if p.Key != "k1" || len(p.Keys) != 1 || p.Keys[0].Key != "k2" || p.Keys[0].Off {
		t.Fatalf("after add: key %q keys %+v", p.Key, p.Keys)
	}
	if p.Keys[0].Name != "team" || p.Keys[0].Protocol != Anthropic {
		t.Errorf("new key lost its name or protocol: %+v", p.Keys[0])
	}
}

func TestS2KUseKeyKeepsOthers(t *testing.T) {
	s2kSandbox(t)
	s2kRelay(t, "k1")
	if err := AddKey("s2krelay", "", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("s2krelay", "", "k3", Chat); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("s2krelay", KeyRef("k3")); err != nil {
		t.Fatal(err)
	}
	p := s2kFind(t)
	if p.Key != "k3" || p.KeyProtocol != Chat {
		t.Fatalf("current key after UseKey: %q %q", p.Key, p.KeyProtocol)
	}
	if len(p.Keys) != 2 || p.Keys[0].Key != "k1" || p.Keys[1].Key != "k2" || p.Keys[1].Protocol != Anthropic {
		t.Fatalf("the others after UseKey: %+v", p.Keys)
	}
	if err := UseKey("s2krelay", KeyRef("k2")); err != nil {
		t.Fatal(err)
	}
	p = s2kFind(t)
	if p.Key != "k2" || p.KeyProtocol != Anthropic || len(p.Keys) != 2 || p.Keys[0].Key != "k3" || p.Keys[0].Protocol != Chat {
		t.Errorf("second UseKey: key %q %q keys %+v", p.Key, p.KeyProtocol, p.Keys)
	}
}

func TestS2KDisableRemoveRules(t *testing.T) {
	s2kSandbox(t)
	s2kRelay(t, "k1")
	AddKey("s2krelay", "b", "k2", "")
	AddKey("s2krelay", "c", "k3", "")

	if err := SetKeyOn("s2krelay", KeyRef("k1"), false); err != nil {
		t.Fatal(err)
	}
	p := s2kFind(t)
	if p.Key != "k2" || p.KeyName != "b" {
		t.Fatalf("disabling the current key should hand over to k2, got %q %q", p.Key, p.KeyName)
	}
	if len(p.Keys) != 2 || p.Keys[0].Key != "k1" || !p.Keys[0].Off {
		t.Fatalf("the disabled first key should stay, off: %+v", p.Keys)
	}

	if err := SetKeyOn("s2krelay", KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("s2krelay", KeyRef("k3"), false); err == nil {
		t.Fatal("the last enabled key was disabled")
	}
	if p := s2kFind(t); p.Key != "k3" {
		t.Fatalf("refused disable changed the current key to %q", p.Key)
	}

	if err := SetKeyOn("s2krelay", KeyRef("k1"), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("s2krelay", KeyRef("k3")); err != nil {
		t.Fatal(err)
	}
	p = s2kFind(t)
	if p.Key != "k1" {
		t.Fatalf("removing the current key should hand over to k1, got %q", p.Key)
	}
	if err := RemoveKey("s2krelay", KeyRef("k1")); err == nil {
		t.Fatal("the last enabled key was removed")
	}
	if err := RemoveKey("s2krelay", KeyRef("k2")); err != nil {
		t.Fatal(err)
	}
	if p := s2kFind(t); p.Key != "k1" || len(p.Keys) != 0 {
		t.Errorf("after removing the disabled key: %q %+v", p.Key, p.Keys)
	}
}

func TestS2KRenameAndProtocol(t *testing.T) {
	s2kSandbox(t)
	s2kRelay(t, "k1")
	AddKey("s2krelay", "", "k2", "")
	if err := RenameKey("s2krelay", KeyRef("k1"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("s2krelay", KeyRef("k2"), "team"); err != nil {
		t.Fatal(err)
	}
	p := s2kFind(t)
	if p.KeyName != "personal" || p.Keys[0].Name != "team" {
		t.Errorf("names after rename: %q %+v", p.KeyName, p.Keys)
	}
	if err := SetKeyProtocol("s2krelay", KeyRef("k2"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("s2krelay", KeyRef("k2"), "gemini"); err == nil {
		t.Error("an unknown protocol was accepted")
	}
	if p := s2kFind(t); p.Keys[0].Protocol != Anthropic {
		t.Errorf("protocol after set: %q", p.Keys[0].Protocol)
	}
	if err := SetKeyProtocol("s2krelay", KeyRef("k2"), ""); err != nil {
		t.Fatal(err)
	}
	if p := s2kFind(t); p.Keys[0].Protocol != "" {
		t.Errorf("protocol after clearing: %q", p.Keys[0].Protocol)
	}
	if err := RenameKey("s2krelay", "0000000000", "x"); err == nil {
		t.Error("an unknown key reference was accepted")
	}
}

func TestS2KSaveRejectsBadKeyProtocol(t *testing.T) {
	s2kSandbox(t)
	err := Save(Provider{ID: "s2kbad", Name: "bad", Key: "k", Chat: "https://bad.example/v1", KeyProtocol: "gemini"})
	if err == nil {
		t.Error("a key protocol of gemini was saved")
	}
}

func TestS2KCandidatesFollowProtocolRules(t *testing.T) {
	p := Provider{ID: "mix", Name: "mix", Chat: "https://mix.example/v1", Anthropic: "https://mix.example",
		Key: "any", Keys: []KeyEntry{
			{Key: "oa", Protocol: Chat},
			{Key: "an", Protocol: Anthropic},
			{Key: "dis", Off: true},
		}}
	keysOf := func(cs []Provider) string {
		var ks []string
		for _, c := range cs {
			ks = append(ks, c.Key)
		}
		return strings.Join(ks, ",")
	}
	if got := keysOf(p.KeyCandidates(Chat, "claude-sonnet-5-5")); got != "an,any,oa" {
		t.Errorf("claude model from chat: %s", got)
	}
	if got := keysOf(p.KeyCandidates(Anthropic, "gpt-5.5")); got != "oa,any,an" {
		t.Errorf("gpt model from anthropic: %s", got)
	}
	if got := keysOf(p.KeyCandidates(Chat, "anthropic/claude-x")); got != "an,any,oa" {
		t.Errorf("prefixed claude model: %s", got)
	}
	if got := keysOf(p.KeyCandidates(Chat, "deepseek-chat")); got != "any,oa,an" {
		t.Errorf("other model from chat: %s", got)
	}

	// a key limited to a protocol the vendor lacks does not take part
	chatOnly := Provider{ID: "c", Name: "c", Chat: "https://c.example/v1", Key: "x", Keys: []KeyEntry{
		{Key: "an", Protocol: Anthropic}}}
	if got := keysOf(chatOnly.KeyCandidates(Chat, "claude-x")); got != "x" {
		t.Errorf("anthropic key on a chat-only vendor: %s", got)
	}

	// a keyless provider is its own only candidate
	local := Provider{ID: "l", Name: "l", Chat: "http://localhost:11434/v1"}
	if got := local.KeyCandidates(Chat, "m"); len(got) != 1 {
		t.Errorf("keyless provider: %d candidates", len(got))
	}
}

func TestS2KKeyForNeverCrossesProtocols(t *testing.T) {
	p := Provider{ID: "mix", Name: "mix", Chat: "https://mix.example/v1", Anthropic: "https://mix.example",
		Key: "oa", KeyProtocol: Chat, Keys: []KeyEntry{{Key: "an", Protocol: Anthropic}}}
	if c, ok := p.KeyFor(Anthropic); !ok || c.Key != "an" {
		t.Errorf("anthropic endpoint got %q %v", c.Key, ok)
	}
	if c, ok := p.KeyFor(Chat); !ok || c.Key != "oa" {
		t.Errorf("chat endpoint got %q %v", c.Key, ok)
	}
	onlyChat := Provider{ID: "o", Name: "o", Chat: "https://o.example/v1", Anthropic: "https://o.example",
		Key: "oa", KeyProtocol: Chat}
	if _, ok := onlyChat.KeyFor(Anthropic); ok {
		t.Error("an OpenAI-only key was offered to the anthropic endpoint")
	}
}

func TestS2KRestingKeyAndKeyRef(t *testing.T) {
	if got := KeyRef("k1"); len(got) != 10 || got != strings.ToLower(got) {
		t.Errorf("KeyRef should be 10 lowercase hex characters, got %q", got)
	}
	if KeyRef("k1") == KeyRef("k2") {
		t.Error("two keys share a reference")
	}
}
