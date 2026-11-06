package provider

import (
	"regexp"

	"github.com/yetone/magpie/internal/catalog"
	"testing"
)

// kpKeyedRelay saves a provider "relay" whose first key is k1, with one endpoint.
func kpKeyedRelay(t *testing.T) string {
	t.Helper()
	kpIsolateHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	return "relay"
}

func kpMustFind(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func TestKeyRefIsTenLowercaseHexChars(t *testing.T) {
	ref := KeyRef("sk-secret")
	if !regexp.MustCompile(`^[0-9a-f]{10}$`).MatchString(ref) {
		t.Fatalf("KeyRef = %q, want 10 lowercase hex chars", ref)
	}
	if KeyRef("sk-secret") != ref || KeyRef("other") == ref {
		t.Error("KeyRef is not a stable fingerprint of the key")
	}
}

func TestAddKeyFirstBecomesCurrent(t *testing.T) {
	kpIsolateHome(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "first", "abc", ""); err != nil {
		t.Fatal(err)
	}
	p := kpMustFind(t, "local")
	if p.Key != "abc" || p.KeyName != "first" || len(p.Keys) != 0 {
		t.Fatalf("first key = %+v", p)
	}
}

func TestAddKeyAppendsEnabledAndRejects(t *testing.T) {
	id := kpKeyedRelay(t)
	if err := AddKey(id, "team", "k2", Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey(id, "dup", "k1", ""); err == nil {
		t.Error("duplicate of the key in use accepted")
	}
	if err := AddKey(id, "dup2", "k2", ""); err == nil {
		t.Error("duplicate of another key accepted")
	}
	if err := AddKey(id, "bad", "k3", Gemini); err == nil {
		t.Error("gemini accepted as a key protocol")
	}
	if err := AddKey(id, "bad", "k3", "gpt"); err == nil {
		t.Error("unknown protocol accepted")
	}
	if err := AddKey(id, "empty", "  ", ""); err == nil {
		t.Error("empty key accepted")
	}
	p := kpMustFind(t, id)
	if p.Key != "k1" || len(p.Keys) != 1 || p.Keys[0].Key != "k2" || p.Keys[0].Off || p.Keys[0].Protocol != Chat {
		t.Fatalf("keys after adds = %+v", p.Keys)
	}
}

func TestUseKeyKeepsOthersAndTheirLimits(t *testing.T) {
	id := kpKeyedRelay(t)
	if err := RenameKey(id, KeyRef("k1"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey(id, "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey(id, "backup", "k3", Chat); err != nil {
		t.Fatal(err)
	}
	if err := UseKey(id, KeyRef("k3")); err != nil {
		t.Fatal(err)
	}
	p := kpMustFind(t, id)
	if p.Key != "k3" || p.KeyName != "backup" || p.KeyProtocol != Chat {
		t.Fatalf("in use = %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	if len(p.Keys) != 2 || p.Keys[0].Key != "k1" || p.Keys[0].Name != "personal" || p.Keys[0].Off ||
		p.Keys[1].Key != "k2" || p.Keys[1].Protocol != Anthropic {
		t.Fatalf("others = %+v", p.Keys)
	}
}

func TestSetKeyOnRules(t *testing.T) {
	id := kpKeyedRelay(t)
	if err := AddKey(id, "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn(id, KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	if p := kpMustFind(t, id); p.Key != "k1" || !p.Keys[0].Off {
		t.Fatalf("after disabling k2: %+v", p)
	}
	// k1 is the only enabled key now: it cannot be turned off
	if err := SetKeyOn(id, KeyRef("k1"), false); err == nil {
		t.Fatal("last enabled key was disabled")
	}
	if p := kpMustFind(t, id); p.Key != "k1" || len(p.Keys) != 1 || !p.Keys[0].Off {
		t.Fatalf("failed disable changed the file: %+v", p)
	}
	if err := SetKeyOn(id, KeyRef("k2"), true); err != nil {
		t.Fatal(err)
	}
	// with k2 enabled, k1 may step aside; k2 takes over, k1 is kept off
	if err := SetKeyOn(id, KeyRef("k1"), false); err != nil {
		t.Fatal(err)
	}
	p := kpMustFind(t, id)
	if p.Key != "k2" || len(p.Keys) != 1 || p.Keys[0].Key != "k1" || !p.Keys[0].Off {
		t.Fatalf("after k1 steps aside: %+v", p)
	}
	if err := SetKeyOn(id, KeyRef("k2"), false); err == nil {
		t.Fatal("last enabled key was disabled after takeover")
	}
	if err := SetKeyOn(id, KeyRef("nope"), false); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestRemoveKeyRules(t *testing.T) {
	id := kpKeyedRelay(t)
	if err := AddKey(id, "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey(id, KeyRef("k2")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey(id, KeyRef("k1")); err == nil {
		t.Fatal("last enabled key was removed")
	}
	if err := AddKey(id, "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey(id, KeyRef("k1")); err != nil {
		t.Fatal(err)
	}
	p := kpMustFind(t, id)
	if p.Key != "k2" || len(p.Keys) != 0 {
		t.Fatalf("after removing the key in use: %+v", p)
	}
}

func TestRenameAndSetKeyProtocol(t *testing.T) {
	id := kpKeyedRelay(t)
	if err := AddKey(id, "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey(id, KeyRef("k2"), "shared"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol(id, KeyRef("k1"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol(id, KeyRef("k2"), Gemini); err == nil {
		t.Error("gemini accepted as a key protocol")
	}
	p := kpMustFind(t, id)
	if p.Keys[0].Name != "shared" || p.KeyProtocol != Anthropic {
		t.Fatalf("after rename and protocol: %+v", p)
	}
	if err := SetKeyProtocol(id, KeyRef("k1"), ""); err != nil {
		t.Fatal(err)
	}
	if p := kpMustFind(t, id); p.KeyProtocol != "" {
		t.Errorf("protocol not cleared: %q", p.KeyProtocol)
	}
}

func TestSaveRejectsBadKeyProtocolAndKeepsKeys(t *testing.T) {
	id := kpKeyedRelay(t)
	if err := AddKey(id, "team", "k2", Responses); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: id, Name: "Relay", Key: "k1", Chat: "https://relay.example/v1", KeyProtocol: "gemini"}); err == nil {
		t.Error("Save accepted an unknown key protocol")
	}
	// saving again without the key list keeps the other keys
	if err := Save(Provider{ID: id, Name: "Relay", Key: "k1", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	p := kpMustFind(t, id)
	if len(p.Keys) != 1 || p.Keys[0].Key != "k2" || p.Keys[0].Protocol != Responses {
		t.Fatalf("keys after re-save = %+v", p.Keys)
	}
}

func TestKeySlotsAndCandidateOrder(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Key: "ant", KeyProtocol: Anthropic,
		Anthropic: "https://a.example", Chat: "https://c.example/v1",
		Keys: []KeyEntry{{Key: "oai", Protocol: Chat}, {Key: "any"}, {Key: "off", Protocol: Anthropic, Off: true}}}
	order := func(cs []Provider) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Key)
		}
		return out
	}
	eq := func(got []string, want ...string) bool {
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
	if got := order(p.Candidates(Anthropic, "claude-opus-5")); !eq(got, "ant", "any", "oai") {
		t.Errorf("claude from anthropic: %v", got)
	}
	if got := order(p.Candidates(Chat, "gpt-5.5")); !eq(got, "oai", "any", "ant") {
		t.Errorf("gpt from chat: %v", got)
	}
	if got := order(p.Candidates(Anthropic, "deepseek/deepseek-chat")); !eq(got, "ant", "any", "oai") {
		t.Errorf("other from anthropic: %v", got)
	}
	// an Anthropic-limited key is scoped to that endpoint only
	for _, c := range p.Candidates(Chat, "gpt-5.5") {
		if c.Key == "ant" && (c.Chat != "" || c.Anthropic == "") {
			t.Errorf("anthropic-limited key reaches %+v", c)
		}
	}
	// a key whose endpoint the provider lacks does not take part
	noAnt := Provider{ID: "x", Name: "X", Key: "ant", KeyProtocol: Anthropic, Chat: "https://c.example/v1",
		Keys: []KeyEntry{{Key: "oai", Protocol: Chat}}}
	if got := order(noAnt.Candidates(Anthropic, "claude-opus-5")); !eq(got, "oai") {
		t.Errorf("key without endpoint took part: %v", got)
	}
}

func TestModelFamily(t *testing.T) {
	cases := map[string]modelFamily{
		"claude-opus-5":          familyClaude,
		"anthropic/claude-haiku": familyClaude,
		"gpt-5.5":                familyOpenAI,
		"o3-mini":                familyOpenAI,
		"gpt-5-codex":            familyOpenAI,
		"deepseek/deepseek-chat": familyOther,
		"openrouter-thing":       familyOther,
	}
	for m, want := range cases {
		if got := familyOf(m); got != want {
			t.Errorf("familyOf(%q) = %v, want %v", m, got, want)
		}
	}
}

// kpIsolateHome points every magpie path at a fresh temp dir.
func kpIsolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/cfg")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func TestUseKeyRefusesDisabledKey(t *testing.T) {
	kpKeyedRelay(t)
	if err := AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	ref := KeyRef("k2")
	if err := SetKeyOn("relay", ref, false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", ref); err == nil {
		t.Fatal("UseKey promoted a disabled key to the one in use")
	}
	if p := kpMustFind(t, "relay"); p.Key != "k1" {
		t.Fatalf("key in use = %q, want k1", p.Key)
	}
}
