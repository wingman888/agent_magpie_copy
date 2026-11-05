package provider

import (
	"strings"
	"testing"
)

// newRelay saves a relay with one key in use, named "personal".
func newRelay(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "personal",
		Chat: "https://relay.test/v1", Anthropic: "https://relay.test"}); err != nil {
		t.Fatal(err)
	}
}

// stored reads the relay as the file holds it.
func stored(t *testing.T) Provider {
	t.Helper()
	for _, p := range load().Providers {
		if p.ID == "relay" {
			return p
		}
	}
	t.Fatal("relay not stored")
	return Provider{}
}

func names(ks []ExtraKey) string {
	var out []string
	for _, k := range ks {
		s := k.Name
		if k.Off {
			s += "(off)"
		}
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

func TestKeyRefIsTenLowerHex(t *testing.T) {
	ref := KeyRef("sk-anything")
	if len(ref) != 10 {
		t.Fatalf("KeyRef = %q, want 10 characters", ref)
	}
	for _, c := range ref {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("KeyRef = %q is not lower-case hex", ref)
		}
	}
	if KeyRef("sk-anything") != ref || KeyRef("sk-other") == ref {
		t.Fatal("KeyRef must depend on the key only")
	}
}

func TestAddKeyRules(t *testing.T) {
	newRelay(t)
	if err := AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if got := names(stored(t).Keys); got != "team" {
		t.Fatalf("extra keys = %q, want team", got)
	}
	if err := AddKey("relay", "again", "k2", ""); err == nil {
		t.Error("a repeated key must be refused")
	}
	if err := AddKey("relay", "again", "k1", ""); err == nil {
		t.Error("a repeat of the key in use must be refused")
	}
	if err := AddKey("relay", "odd", "k3", Gemini); err == nil {
		t.Error("gemini is not a key protocol")
	}
	if err := AddKey("relay", "odd", "k3", Protocol("nope")); err == nil {
		t.Error("an unknown protocol must be refused")
	}
	if err := AddKey("nosuch", "x", "k4", ""); err == nil {
		t.Error("an unknown provider must be refused")
	}
}

func TestFirstKeyBecomesCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "one", "sk-one", ""); err != nil {
		t.Fatal(err)
	}
	var p Provider
	for _, q := range load().Providers {
		if q.ID == "local" {
			p = q
		}
	}
	if p.Key != "sk-one" || p.KeyName != "one" || len(p.Keys) != 0 {
		t.Fatalf("first key = %+v, want it in use", p)
	}
}

func TestLastEnabledKeyStaysOn(t *testing.T) {
	newRelay(t)
	k1 := KeyRef("k1")
	if err := RemoveKey("relay", k1); err == nil {
		t.Error("the last enabled key must not be removable")
	}
	if err := SetKeyOn("relay", k1, false); err == nil {
		t.Error("the last enabled key must not be turned off")
	}
	if stored(t).Key != "k1" {
		t.Fatal("a refused change must leave the key in use alone")
	}
	if err := AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", k1, false); err != nil {
		t.Fatal(err)
	}
	// k2 has taken over; k1 is now the only disabled key, so it can go
	if p := stored(t); p.Key != "k2" || names(p.Keys) != "personal(off)" {
		t.Fatalf("after turning k1 off: key %q, extras %q", p.Key, names(p.Keys))
	}
	if err := RemoveKey("relay", KeyRef("k2")); err == nil {
		t.Error("removing the only enabled key must be refused")
	}
	if err := RemoveKey("relay", k1); err != nil {
		t.Fatal(err)
	}
	if p := stored(t); p.Key != "k2" || len(p.Keys) != 0 {
		t.Fatalf("after removing k1: %+v", p)
	}
}

func TestUseKeyKeepsOthers(t *testing.T) {
	newRelay(t)
	if err := AddKey("relay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "backup", "k3", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("k3"), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("k2")); err != nil {
		t.Fatal(err)
	}
	p := stored(t)
	if p.Key != "k2" || p.KeyName != "team" || p.KeyProtocol != Anthropic {
		t.Fatalf("in use = %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	if names(p.Keys) != "personal,backup(off)" {
		t.Fatalf("others = %q", names(p.Keys))
	}
	if p.Keys[0].Key != "k1" {
		t.Fatalf("the replaced key must come right behind: %+v", p.Keys)
	}
	if err := UseKey("relay", KeyRef("nope")); err == nil {
		t.Error("an unknown key must be refused")
	}
}

func TestTurningOffCurrentHandsOver(t *testing.T) {
	newRelay(t)
	if err := AddKey("relay", "team", "k2", Chat); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "backup", "k3", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("k1"), false); err != nil {
		t.Fatal(err)
	}
	p := stored(t)
	if p.Key != "k2" || p.KeyName != "team" || p.KeyProtocol != Chat {
		t.Fatalf("hand-over went to %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	if names(p.Keys) != "personal(off),backup" {
		t.Fatalf("keys = %q", names(p.Keys))
	}
	if err := SetKeyOn("relay", KeyRef("k3"), true); err != nil {
		t.Fatal(err)
	}
	if names(stored(t).Keys) != "personal(off),backup" {
		t.Fatal("turning a key on must not reorder the others")
	}
}

func TestSetKeyProtocolAndRename(t *testing.T) {
	newRelay(t)
	if err := AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("k2"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("k2"), Gemini); err == nil {
		t.Error("gemini is not a key protocol")
	}
	if p := stored(t); p.Keys[0].Protocol != Anthropic {
		t.Fatalf("protocol = %q", p.Keys[0].Protocol)
	}
	if err := SetKeyProtocol("relay", KeyRef("k2"), ""); err != nil {
		t.Fatal(err)
	}
	if p := stored(t); p.Keys[0].Protocol != "" {
		t.Fatalf("protocol not cleared: %q", p.Keys[0].Protocol)
	}
	if err := RenameKey("relay", KeyRef("k2"), "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", KeyRef("k1"), "  "); err == nil {
		t.Error("an empty name must be refused")
	}
	if err := RenameKey("relay", KeyRef("k1"), "mine"); err != nil {
		t.Fatal(err)
	}
	if p := stored(t); p.KeyName != "mine" || p.Keys[0].Name != "renamed" {
		t.Fatalf("names = %q, %q", p.KeyName, p.Keys[0].Name)
	}
}

func TestSaveKeepsOtherKeys(t *testing.T) {
	newRelay(t)
	if err := AddKey("relay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	// saving again without the keys leaves them as they were
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "https://relay.test/v1"}); err != nil {
		t.Fatal(err)
	}
	if p := stored(t); names(p.Keys) != "team" || p.KeyName != "personal" {
		t.Fatalf("after re-save: keys %q, name %q", names(p.Keys), p.KeyName)
	}
	// a key the user puts in place of the current one takes its own name
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k2", Chat: "https://relay.test/v1"}); err != nil {
		t.Fatal(err)
	}
	if p := stored(t); p.Key != "k2" || p.KeyName != "team" || p.KeyProtocol != Anthropic || len(p.Keys) != 0 {
		t.Fatalf("after replacing the key: %+v", p)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k2", KeyProtocol: Gemini, Chat: "https://relay.test/v1"}); err == nil {
		t.Error("a key protocol magpie cannot speak must be refused")
	}
}

func TestSaveDropsBlankAndRepeatedExtraKeys(t *testing.T) {
	newRelay(t)
	err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "personal", Chat: "https://relay.test/v1",
		Keys: []ExtraKey{{Key: "  "}, {Key: "k1"}, {Name: "x", Key: " k9 "}, {Name: "x", Key: "k9"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p := stored(t); names(p.Keys) != "x" || p.Keys[0].Key != "k9" {
		t.Fatalf("extra keys = %+v", p.Keys)
	}
}

func TestCandidatesOrder(t *testing.T) {
	p := Provider{ID: "relay", Chat: "https://r.test/v1", Anthropic: "https://r.test",
		Key: "c1", KeyName: "oai", KeyProtocol: Chat,
		Keys: []ExtraKey{{Name: "ant", Key: "a1", Protocol: Anthropic}, {Name: "plain", Key: "p1"}, {Name: "off", Key: "o1", Off: true}}}
	cases := []struct {
		model string
		from  Protocol
		want  string
	}{
		{"claude-x", Chat, "ant,plain,oai"},
		{"gpt-5", Anthropic, "oai,plain,ant"},
		{"o3-mini", Anthropic, "oai,plain,ant"},
		{"openai/gpt-5-codex", Anthropic, "oai,plain,ant"},
		{"deepseek-chat", Anthropic, "ant,plain,oai"},
	}
	for _, c := range cases {
		var got []string
		for _, q := range p.Candidates(c.model, c.from) {
			got = append(got, q.KeyName)
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("Candidates(%s, %s) = %v, want %s", c.model, c.from, got, c.want)
		}
	}
	// a key limited to an endpoint the provider lacks is left out
	chatOnly := p
	chatOnly.Anthropic = ""
	for _, q := range chatOnly.Candidates("claude-x", Chat) {
		if q.KeyName == "ant" {
			t.Error("a key for a missing endpoint must not be a candidate")
		}
	}
	// the candidate carries its own key everywhere a key is read
	for _, q := range p.Candidates("claude-x", Chat) {
		if q.KeyName == "ant" && (q.Key != "a1" || q.KeyProtocol != Anthropic || len(q.Keys) != 0) {
			t.Errorf("candidate %+v does not carry its key", q)
		}
	}
}

func TestModelFamily(t *testing.T) {
	for model, want := range map[string]string{
		"claude-opus-5-5": "claude", "anthropic/claude-x": "claude",
		"gpt-5.5": "openai", "o1": "openai", "o3-mini": "openai", "codex-mini": "openai",
		"deepseek-chat": "", "openai-compat": "",
	} {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %q, want %q", model, got, want)
		}
	}
}
