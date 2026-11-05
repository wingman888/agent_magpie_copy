package provider

import (
	"reflect"
	"testing"
)

// keyed is a provider with a current key "sk-a" (named "a") and two extra
// keys; it is saved so the store functions see it.
func keyed(t *testing.T) {
	t.Helper()
	useConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-a", KeyName: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "b", "sk-b", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "c", "sk-c", ""); err != nil {
		t.Fatal(err)
	}
}

func stored(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func names(ks []ExtraKey) []string {
	var out []string
	for _, k := range ks {
		out = append(out, k.Name)
	}
	return out
}

func TestAddKeyFirstBecomesCurrent(t *testing.T) {
	useConfig(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "one", "sk-1", ""); err != nil {
		t.Fatal(err)
	}
	p := stored(t, "local")
	if p.Key != "sk-1" || p.KeyName != "one" || len(p.Keys) != 0 {
		t.Fatalf("first key: %+v", p)
	}
	if err := AddKey("local", "two", "sk-2", Anthropic); err != nil {
		t.Fatal(err)
	}
	p = stored(t, "local")
	if p.Key != "sk-1" || len(p.Keys) != 1 || p.Keys[0].Key != "sk-2" || p.Keys[0].Protocol != Anthropic || p.Keys[0].Off {
		t.Fatalf("second key: %+v", p)
	}
}

func TestAddKeyRejectsDuplicatesAndUnknownProtocol(t *testing.T) {
	keyed(t)
	if err := AddKey("relay", "again", "sk-b", ""); err == nil {
		t.Error("duplicate extra key accepted")
	}
	if err := AddKey("relay", "again", "sk-a", ""); err == nil {
		t.Error("duplicate current key accepted")
	}
	if err := AddKey("relay", "bad", "sk-d", Gemini); err == nil {
		t.Error("gemini accepted as a key protocol")
	}
	if err := AddKey("relay", "bad", "sk-d", Protocol("openai")); err == nil {
		t.Error("unknown protocol accepted")
	}
	if got := len(stored(t, "relay").Keys); got != 2 {
		t.Errorf("%d extra keys after rejected adds, want 2", got)
	}
}

func TestUseKeyKeepsTheOthers(t *testing.T) {
	keyed(t)
	if err := SetKeyProtocol("relay", KeyRef("sk-b"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("sk-c")); err != nil {
		t.Fatal(err)
	}
	p := stored(t, "relay")
	if p.Key != "sk-c" || p.KeyName != "c" || p.KeyProtocol != "" {
		t.Fatalf("current after UseKey: %+v", p)
	}
	if !reflect.DeepEqual(names(p.Keys), []string{"a", "b"}) {
		t.Fatalf("extra keys %v, want [a b]", names(p.Keys))
	}
	if p.Keys[0].Key != "sk-a" || p.Keys[1].Key != "sk-b" || p.Keys[1].Protocol != Anthropic {
		t.Fatalf("extra keys lost their values or protocol: %+v", p.Keys)
	}
}

func TestSetKeyOnPromotesAndGuardsTheLast(t *testing.T) {
	keyed(t)
	if err := SetKeyOn("relay", KeyRef("sk-a"), false); err != nil {
		t.Fatal(err)
	}
	p := stored(t, "relay")
	if p.Key != "sk-b" || p.KeyName != "b" {
		t.Fatalf("next enabled key did not take over: %+v", p)
	}
	if len(p.Keys) != 2 || p.Keys[0].Key != "sk-a" || !p.Keys[0].Off {
		t.Fatalf("disabled first key not kept: %+v", p.Keys)
	}
	if err := SetKeyOn("relay", KeyRef("sk-c"), false); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("sk-b"), false); err == nil {
		t.Fatal("the last enabled key was disabled")
	}
	if got := stored(t, "relay").Key; got != "sk-b" {
		t.Fatalf("failed disable changed the current key to %q", got)
	}
	choices := stored(t, "relay").KeyChoices()
	if len(choices) != 1 || choices[0].Key != "sk-b" {
		t.Fatalf("disabled keys still offered: %+v", choices)
	}
	if err := SetKeyOn("relay", KeyRef("sk-a"), true); err != nil {
		t.Fatal(err)
	}
	if got := len(stored(t, "relay").KeyChoices()); got != 2 {
		t.Fatalf("%d enabled keys after re-enabling, want 2", got)
	}
}

func TestRemoveKey(t *testing.T) {
	keyed(t)
	if err := RemoveKey("relay", KeyRef("sk-a")); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, "relay").Key; got != "sk-b" {
		t.Fatalf("current after removing the first key = %q, want sk-b", got)
	}
	if err := RemoveKey("relay", KeyRef("sk-b")); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, "relay").Key; got != "sk-c" {
		t.Fatalf("current after removing the second key = %q, want sk-c", got)
	}
	if err := RemoveKey("relay", KeyRef("sk-c")); err == nil {
		t.Fatal("the last enabled key was removed")
	}
	if got := stored(t, "relay").Key; got != "sk-c" {
		t.Fatalf("failed removal changed the key to %q", got)
	}
}

func TestRenameKey(t *testing.T) {
	keyed(t)
	if err := RenameKey("relay", KeyRef("sk-a"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", KeyRef("sk-b"), "team"); err != nil {
		t.Fatal(err)
	}
	p := stored(t, "relay")
	if p.KeyName != "personal" || p.Keys[0].Name != "team" {
		t.Fatalf("renamed: %+v", p)
	}
	if err := RenameKey("relay", KeyRef("sk-b"), "  "); err == nil {
		t.Error("empty name accepted")
	}
	if err := RenameKey("relay", "0000000000", "x"); err == nil {
		t.Error("unknown key accepted")
	}
}

func TestSetKeyProtocolRejectsUnknown(t *testing.T) {
	keyed(t)
	if err := SetKeyProtocol("relay", KeyRef("sk-b"), Protocol("gemini")); err == nil {
		t.Error("gemini accepted")
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-b"), Responses); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("sk-b"), ""); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, "relay").Keys[0].Protocol; got != "" {
		t.Fatalf("protocol not cleared: %q", got)
	}
}

func TestAttemptsOrderByModelFamily(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://c.example/v1", Anthropic: "https://a.example",
		Key: "sk-oa", KeyName: "openai", KeyProtocol: Chat,
		Keys: []ExtraKey{{Name: "anth", Key: "sk-an", Protocol: Anthropic}}}

	// a Claude model from Claude Code: the Anthropic key first; the OpenAI key
	// is a last resort and goes to Chat only
	atts := p.Attempts(Anthropic, "claude-sonnet-5")
	if len(atts) != 2 || atts[0].Key.Name != "anth" || atts[0].To != Anthropic || atts[1].To != Chat {
		t.Fatalf("claude from anthropic: %+v", atts)
	}
	// a Claude model from an OpenAI client: the Anthropic key, translated
	atts = p.Attempts(Chat, "anthropic/claude-opus-5-5")
	if atts[0].Key.Name != "anth" || atts[0].To != Anthropic {
		t.Fatalf("claude from chat: %+v", atts)
	}
	// a GPT model from Claude Code: the OpenAI key first, translated
	atts = p.Attempts(Anthropic, "gpt-5.5")
	if len(atts) != 2 || atts[0].Key.Name != "openai" || atts[0].To != Chat || atts[1].To != Anthropic {
		t.Fatalf("gpt from anthropic: %+v", atts)
	}
	// an o-series model from Chat: the OpenAI key, not translated
	atts = p.Attempts(Chat, "o4-mini")
	if atts[0].Key.Name != "openai" || atts[0].To != Chat {
		t.Fatalf("o-series from chat: %+v", atts)
	}
	// any other model prefers the client's own protocol
	atts = p.Attempts(Chat, "deepseek-chat")
	if atts[0].Key.Name != "openai" || atts[0].To != Chat {
		t.Fatalf("other model from chat: %+v", atts)
	}
}

func TestAttemptsSkipUnusableAndDisabledKeys(t *testing.T) {
	p := Provider{ID: "relay", Name: "Relay", Anthropic: "https://a.example", Key: "sk-a",
		Keys: []ExtraKey{
			{Name: "off", Key: "sk-off", Off: true},
			{Name: "chat-only", Key: "sk-chat", Protocol: Chat},
			{Name: "team", Key: "sk-team"},
		}}
	atts := p.Attempts(Anthropic, "claude-sonnet-5")
	if len(atts) != 2 || atts[0].Key.Key != "sk-a" || atts[1].Key.Key != "sk-team" {
		t.Fatalf("attempts %+v: disabled or endpoint-less key was used", atts)
	}
	for _, a := range atts {
		if a.Key.Key == "sk-chat" || a.Key.Key == "sk-off" {
			t.Fatalf("unusable key attempted: %+v", a)
		}
	}
	// a single key limited to an endpoint the provider lacks has no attempt
	single := Provider{ID: "x", Name: "X", Anthropic: "https://a.example", Key: "sk-c", KeyProtocol: Chat}
	if got := single.Attempts(Anthropic, "claude"); len(got) != 0 {
		t.Fatalf("chat-only key attempted on a provider without chat: %+v", got)
	}
}
