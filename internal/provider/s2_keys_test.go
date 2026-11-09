package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// s2Isolate points HOME, the config and the cache at fresh directories so the
// test never touches the machine's agents or real providers.json.
func s2Isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// s2Relay saves a custom relay with an OpenAI-style and an Anthropic endpoint
// and the given first key.
func s2Relay(t *testing.T, key string) {
	t.Helper()
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: key,
		Chat: "https://relay.example.com/v1", Anthropic: "https://relay.example.com"}); err != nil {
		t.Fatal(err)
	}
}

// s2Stored reads the relay back the way the gateway sees it.
func s2Stored(t *testing.T) Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestS2KeyFingerprint(t *testing.T) {
	sum := sha256.Sum256([]byte("sk-s2-abc"))
	want := hex.EncodeToString(sum[:5])
	if got := KeyRef("sk-s2-abc"); got != want || len(got) != 10 {
		t.Fatalf("KeyRef = %q, want %q (10 hex chars)", got, want)
	}
}

func TestS2AddKeyRules(t *testing.T) {
	s2Isolate(t)
	s2Relay(t, "sk-s2-one")
	if err := AddKey("relay", "dup", "sk-s2-one", ""); err == nil {
		t.Error("a repeated key was accepted")
	}
	if err := AddKey("relay", "bad", "sk-s2-x", "gemini"); err == nil {
		t.Error("gemini was accepted as a key protocol")
	}
	if err := AddKey("relay", "empty", "   ", ""); err == nil {
		t.Error("an empty key was accepted")
	}
	if err := AddKey("relay", "team", "sk-s2-two", Anthropic); err != nil {
		t.Fatal(err)
	}
	chain := s2Stored(t).keyChain()
	if len(chain) != 2 || chain[0].Key != "sk-s2-one" || chain[1].Key != "sk-s2-two" ||
		chain[1].Name != "team" || chain[1].Protocol != Anthropic || chain[1].Off {
		t.Fatalf("chain after AddKey = %+v", chain)
	}

	// a keyless local vendor: the first key added becomes the current one
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:9999/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "first", "sk-s2-local", ""); err != nil {
		t.Fatal(err)
	}
	p, err := Find("local")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-s2-local" || p.KeyName != "first" || len(p.Keys) != 0 {
		t.Fatalf("first key = %q named %q, extras %v", p.Key, p.KeyName, p.Keys)
	}
}

func TestS2LastEnabledKeyStays(t *testing.T) {
	s2Isolate(t)
	s2Relay(t, "sk-s2-one")
	if err := AddKey("relay", "two", "sk-s2-two", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-s2-two"), false); err != nil {
		t.Fatalf("disabling a spare key: %v", err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-s2-one"), false); err == nil {
		t.Error("the last enabled key was disabled")
	}
	if err := RemoveKey("relay", KeyRef("sk-s2-one")); err == nil {
		t.Error("the last enabled key was removed")
	}
	p := s2Stored(t)
	if p.Key != "sk-s2-one" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-s2-two" || !p.Keys[0].Off {
		t.Fatalf("keys changed by refused edits: key=%q keys=%+v", p.Key, p.Keys)
	}
	if err := RemoveKey("relay", KeyRef("sk-s2-two")); err != nil {
		t.Fatalf("removing a disabled key: %v", err)
	}
	if got := s2Stored(t).keyChain(); len(got) != 1 {
		t.Fatalf("after removing the spare key: %+v", got)
	}
}

func TestS2UseKeyKeepsOthers(t *testing.T) {
	s2Isolate(t)
	s2Relay(t, "sk-s2-one")
	if err := RenameKey("relay", KeyRef("sk-s2-one"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-s2-one"), Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "team", "sk-s2-two", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "backup", "sk-s2-three", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-s2-three"), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("sk-s2-two")); err != nil {
		t.Fatal(err)
	}
	p := s2Stored(t)
	if p.Key != "sk-s2-two" || p.KeyName != "team" || p.KeyProtocol != Anthropic {
		t.Fatalf("current key = %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	chain := p.keyChain()
	if len(chain) != 3 {
		t.Fatalf("chain = %+v", chain)
	}
	if chain[1].Key != "sk-s2-one" || chain[1].Name != "personal" || chain[1].Protocol != Chat || chain[1].Off {
		t.Errorf("previous current key lost its name, limit or enabled state: %+v", chain[1])
	}
	if chain[2].Key != "sk-s2-three" || chain[2].Name != "backup" || !chain[2].Off {
		t.Errorf("disabled key changed: %+v", chain[2])
	}
	if err := UseKey("relay", KeyRef("sk-s2-missing")); err == nil {
		t.Error("UseKey accepted an unknown key")
	}
}

func TestS2DisablingCurrentHandsOver(t *testing.T) {
	s2Isolate(t)
	s2Relay(t, "sk-s2-one")
	AddKey("relay", "two", "sk-s2-two", "")
	AddKey("relay", "three", "sk-s2-three", "")
	if err := SetKeyOn("relay", KeyRef("sk-s2-one"), false); err != nil {
		t.Fatal(err)
	}
	chain := s2Stored(t).keyChain()
	if len(chain) != 3 || chain[0].Key != "sk-s2-two" || chain[0].Off ||
		chain[1].Key != "sk-s2-one" || !chain[1].Off || chain[2].Key != "sk-s2-three" || chain[2].Off {
		t.Fatalf("after disabling the current key: %+v", chain)
	}
	if err := SetKeyOn("relay", KeyRef("sk-s2-one"), true); err != nil {
		t.Fatal(err)
	}
	if chain := s2Stored(t).keyChain(); chain[1].Off {
		t.Errorf("re-enabled key still off: %+v", chain[1])
	}
}

func TestS2RemoveCurrentHandsOver(t *testing.T) {
	s2Isolate(t)
	s2Relay(t, "sk-s2-one")
	AddKey("relay", "two", "sk-s2-two", "")
	AddKey("relay", "three", "sk-s2-three", "")
	SetKeyOn("relay", KeyRef("sk-s2-two"), false)
	if err := RemoveKey("relay", KeyRef("sk-s2-one")); err != nil {
		t.Fatal(err)
	}
	p := s2Stored(t)
	if p.Key != "sk-s2-three" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-s2-two" || !p.Keys[0].Off {
		t.Fatalf("after removing the current key: key=%q keys=%+v", p.Key, p.Keys)
	}
}

func TestS2RenameAndProtocol(t *testing.T) {
	s2Isolate(t)
	s2Relay(t, "sk-s2-one")
	AddKey("relay", "", "sk-s2-two", "")
	if err := RenameKey("relay", KeyRef("sk-s2-one"), "main"); err != nil {
		t.Fatal(err)
	}
	if p := s2Stored(t); p.KeyName != "main" {
		t.Fatalf("current key name = %q", p.KeyName)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-s2-two"), Responses); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-s2-two"), "gemini"); err == nil {
		t.Error("gemini was accepted as a key protocol")
	}
	if p := s2Stored(t); p.Keys[0].Protocol != Responses {
		t.Fatalf("extra key protocol = %q", p.Keys[0].Protocol)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-s2-two"), ""); err != nil {
		t.Fatal(err)
	}
	if p := s2Stored(t); p.Keys[0].Protocol != "" {
		t.Errorf("protocol limit not cleared: %q", p.Keys[0].Protocol)
	}
	if err := Save(Provider{ID: "bad", Name: "Bad", Key: "sk-s2-z", Chat: "https://bad.example.com/v1", KeyProtocol: "gemini"}); err == nil {
		t.Error("Save accepted a gemini key protocol")
	}
}

func TestS2AttemptsByProtocol(t *testing.T) {
	claude := Provider{Key: "sk-s2-claude", KeyProtocol: Anthropic}
	openai := Provider{Key: "sk-s2-openai", KeyProtocol: Chat}
	// the Anthropic key may be listed first or second; the order must not matter
	for _, first := range []Provider{claude, openai} {
		s2Isolate(t)
		if err := Save(Provider{ID: "relay", Name: "Relay", Key: first.Key, KeyProtocol: first.KeyProtocol,
			Chat: "https://relay.example.com/v1", Anthropic: "https://relay.example.com"}); err != nil {
			t.Fatal(err)
		}
		second := openai
		if first.Key == openai.Key {
			second = claude
		}
		if err := AddKey("relay", "", second.Key, second.KeyProtocol); err != nil {
			t.Fatal(err)
		}
		p := s2Stored(t)

		a := p.Attempts(Anthropic, "claude-sonnet-4")
		if len(a) != 2 || a[0].KeyRef != KeyRef(claude.Key) || a[0].To != Anthropic || a[1].To != Chat {
			t.Errorf("Claude model from Anthropic (first key %s): %+v", first.Key, a)
		}
		a = p.Attempts(Chat, "claude-sonnet-4")
		if len(a) != 2 || a[0].KeyRef != KeyRef(claude.Key) || a[0].To != Anthropic {
			t.Errorf("Claude model from Chat: %+v", a)
		}
		a = p.Attempts(Anthropic, "gpt-5.5")
		if len(a) != 2 || a[0].KeyRef != KeyRef(openai.Key) || a[0].To != Chat || a[1].To != Anthropic {
			t.Errorf("GPT model from Anthropic (first key %s): %+v", first.Key, a)
		}
		a = p.Attempts(Chat, "gpt-5.5")
		if len(a) != 2 || a[0].KeyRef != KeyRef(openai.Key) || a[0].To != Chat {
			t.Errorf("GPT model from Chat: %+v", a)
		}
		for _, a := range p.Attempts(Anthropic, "claude-sonnet-4") {
			if KeyRef(a.Provider.Key) != a.KeyRef {
				t.Errorf("attempt carries the wrong key: %+v", a)
			}
		}
	}
}

func TestS2AttemptsSkipUnusable(t *testing.T) {
	s2Isolate(t)
	// the vendor has no Anthropic endpoint, so an Anthropic-only key is unusable
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-s2-claude", KeyProtocol: Anthropic,
		Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if a := s2Stored(t).Attempts(Chat, "claude-sonnet-4"); len(a) != 0 {
		t.Fatalf("an Anthropic-only key without an Anthropic endpoint was used: %+v", a)
	}
	if err := AddKey("relay", "", "sk-s2-plain", ""); err != nil {
		t.Fatal(err)
	}
	if a := s2Stored(t).Attempts(Chat, "claude-sonnet-4"); len(a) != 1 || a[0].KeyRef != KeyRef("sk-s2-plain") {
		t.Fatalf("attempts with an unlimited key: %+v", a)
	}
	// with the Anthropic-only key still enabled, the unlimited one may go off
	if err := SetKeyOn("relay", KeyRef("sk-s2-plain"), false); err != nil {
		t.Fatalf("disabling a non-last key: %v", err)
	}
	if a := s2Stored(t).Attempts(Chat, "claude-sonnet-4"); len(a) != 0 {
		t.Fatalf("a disabled key was attempted: %+v", a)
	}
}

func TestS2FamilyOf(t *testing.T) {
	for model, want := range map[string]family{
		"claude-sonnet-4":        famClaude,
		"anthropic/claude-opus":  famClaude,
		"gpt-5.5":                famOpenAI,
		"o3-mini":                famOpenAI,
		"gpt-5-codex":            famOpenAI,
		"codex-mini-latest":      famOpenAI,
		"deepseek/deepseek-chat": famOther,
		"qwen-plus":              famOther,
	} {
		if got := familyOf(model); got != want {
			t.Errorf("familyOf(%q) = %v, want %v", model, got, want)
		}
	}
}
