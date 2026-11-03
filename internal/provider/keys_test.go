package provider

import (
	"encoding/hex"
	"strings"
	"testing"
)

// keyedRelay saves a custom provider with one key and both OpenAI and
// Anthropic endpoints, in a temp config dir.
func keyedRelay(t *testing.T, key string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := Save(Provider{ID: "relay", Name: "Relay", Key: key,
		Chat: "https://relay.example.com/v1", Anthropic: "https://relay.example.com"})
	if err != nil {
		t.Fatal(err)
	}
}

func mustFind(t *testing.T) Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// names lists the key values of a provider's key list, current first; a
// disabled key is marked "(off)".
func names(p Provider) []string {
	var out []string
	for _, e := range p.keyList() {
		s := e.Key
		if e.Off {
			s += "(off)"
		}
		out = append(out, s)
	}
	return out
}

func TestKeyRefIsTenHexChars(t *testing.T) {
	ref := KeyRef("sk-abc")
	if len(ref) != 10 {
		t.Fatalf("length %d: %q", len(ref), ref)
	}
	if ref != strings.ToLower(ref) {
		t.Errorf("not lowercase: %q", ref)
	}
	if _, err := hex.DecodeString(ref); err != nil {
		t.Errorf("not hex: %v", err)
	}
	if KeyRef("sk-abc") != ref || KeyRef("sk-abd") == ref {
		t.Errorf("fingerprint should follow the key")
	}
}

func TestAddKeyRejectsDuplicatesAndUnknownProtocol(t *testing.T) {
	keyedRelay(t, "k1")
	if err := AddKey("relay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "again", "k2", ""); err == nil {
		t.Error("duplicate key accepted")
	}
	if err := AddKey("relay", "again", "k1", ""); err == nil {
		t.Error("duplicate of the current key accepted")
	}
	if err := AddKey("relay", "x", "k3", "gemini"); err == nil {
		t.Error("unknown protocol accepted")
	}
	if err := AddKey("relay", "x", "k3", "bogus"); err == nil {
		t.Error("bogus protocol accepted")
	}
	if err := AddKey("nope", "x", "k3", ""); err == nil {
		t.Error("unknown provider accepted")
	}
	p := mustFind(t)
	if p.Key != "k1" || len(p.Keys) != 1 || p.Keys[0].Key != "k2" || p.Keys[0].Protocol != Anthropic || p.Keys[0].Name != "team" {
		t.Errorf("after rejected adds: %+v", p)
	}
}

func TestAddKeyToKeylessProviderBecomesCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:9999/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "first", "sk-1", ""); err != nil {
		t.Fatal(err)
	}
	p, err := Find("local")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "sk-1" || p.KeyName != "first" || len(p.Keys) != 0 {
		t.Errorf("first key: %+v", p)
	}
}

func TestUseKeyKeepsOtherKeysAndProtocols(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, SetKeyProtocol("relay", KeyRef("k1"), Chat))
	must(t, RenameKey("relay", KeyRef("k1"), "personal"))
	must(t, AddKey("relay", "team", "k2", Anthropic))
	must(t, AddKey("relay", "spare", "k3", ""))

	must(t, UseKey("relay", KeyRef("k3")))
	p := mustFind(t)
	if p.Key != "k3" || p.KeyName != "spare" || p.KeyProtocol != "" {
		t.Errorf("current: %+v", p)
	}
	if len(p.Keys) != 2 || p.Keys[0].Key != "k1" || p.Keys[0].Name != "personal" || p.Keys[0].Protocol != Chat ||
		p.Keys[1].Key != "k2" || p.Keys[1].Name != "team" || p.Keys[1].Protocol != Anthropic {
		t.Errorf("others after UseKey: %+v", p.Keys)
	}
	if p.Keys[0].Off || p.Keys[1].Off {
		t.Errorf("UseKey must leave the previous keys enabled")
	}
	// using the current key again changes nothing
	must(t, UseKey("relay", KeyRef("k3")))
	if got := mustFind(t); got.Key != "k3" || len(got.Keys) != 2 {
		t.Errorf("no-op UseKey changed keys: %+v", got)
	}
	if err := UseKey("relay", "0000000000"); err == nil {
		t.Error("unknown key ref accepted")
	}
}

func TestLastEnabledKeyCannotBeDisabledOrRemoved(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, AddKey("relay", "b", "k2", ""))

	must(t, SetKeyOn("relay", KeyRef("k2"), false))
	if err := SetKeyOn("relay", KeyRef("k1"), false); err == nil {
		t.Fatal("last enabled key was disabled")
	}
	if err := RemoveKey("relay", KeyRef("k1")); err == nil {
		t.Fatal("last enabled key was removed")
	}
	p := mustFind(t)
	if p.Key != "k1" || len(p.Keys) != 1 || !p.Keys[0].Off {
		t.Errorf("state changed by rejected calls: %+v", p)
	}
}

func TestDisablingCurrentKeyHandsOver(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, AddKey("relay", "b", "k2", ""))
	must(t, AddKey("relay", "c", "k3", ""))

	must(t, SetKeyOn("relay", KeyRef("k1"), false))
	p := mustFind(t)
	if p.Key != "k2" || p.KeyName != "b" {
		t.Errorf("k2 should take over: %+v", p)
	}
	if got := names(p); strings.Join(got, ",") != "k2,k1(off),k3" {
		t.Errorf("order after takeover: %v", got)
	}

	must(t, SetKeyOn("relay", KeyRef("k1"), true))
	if got := names(mustFind(t)); strings.Join(got, ",") != "k2,k1,k3" {
		t.Errorf("re-enabled: %v", got)
	}
}

func TestRemoveCurrentKeyHandsOver(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, AddKey("relay", "b", "k2", ""))
	must(t, AddKey("relay", "c", "k3", ""))
	must(t, SetKeyOn("relay", KeyRef("k2"), false))

	must(t, RemoveKey("relay", KeyRef("k1")))
	p := mustFind(t)
	if p.Key != "k3" {
		t.Errorf("k3 should take over, got %+v", p)
	}
	if got := names(p); strings.Join(got, ",") != "k3,k2(off)" {
		t.Errorf("after remove: %v", got)
	}
	// a disabled key can go even when it is the only other one
	must(t, RemoveKey("relay", KeyRef("k2")))
	if got := names(mustFind(t)); strings.Join(got, ",") != "k3" {
		t.Errorf("after removing disabled: %v", got)
	}
}

func TestRenameKey(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, AddKey("relay", "b", "k2", ""))
	must(t, RenameKey("relay", KeyRef("k1"), " work "))
	must(t, RenameKey("relay", KeyRef("k2"), "side"))
	p := mustFind(t)
	if p.KeyName != "work" || p.Keys[0].Name != "side" {
		t.Errorf("renamed: %+v", p)
	}
	if err := RenameKey("relay", "0000000000", "x"); err == nil {
		t.Error("unknown key ref accepted")
	}
}

func TestSetKeyProtocolValidates(t *testing.T) {
	keyedRelay(t, "k1")
	if err := SetKeyProtocol("relay", KeyRef("k1"), "gemini"); err == nil {
		t.Error("gemini accepted as a key protocol")
	}
	must(t, SetKeyProtocol("relay", KeyRef("k1"), Anthropic))
	if p := mustFind(t); p.KeyProtocol != Anthropic {
		t.Errorf("protocol: %q", p.KeyProtocol)
	}
	must(t, SetKeyProtocol("relay", KeyRef("k1"), ""))
	if p := mustFind(t); p.KeyProtocol != "" {
		t.Errorf("limit not cleared: %q", p.KeyProtocol)
	}
}

func TestSaveKeepsStoredKeys(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, AddKey("relay", "team", "k2", Chat))
	// an edit that does not mention keys leaves them alone
	if err := Save(Provider{ID: "relay", Name: "Relay 2", Key: "k1", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t)
	if len(p.Keys) != 1 || p.Keys[0].Key != "k2" || p.Keys[0].Name != "team" {
		t.Errorf("keys lost on save: %+v", p.Keys)
	}
	// replacing the current key keeps the others and drops the old one
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k9", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	p = mustFind(t)
	if p.Key != "k9" || len(p.Keys) != 1 || p.Keys[0].Key != "k2" {
		t.Errorf("after replacing current key: %+v", p)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k9", KeyProtocol: "gemini", Chat: "https://relay.example.com/v1"}); err == nil {
		t.Error("save accepted an unknown key protocol")
	}
}

func TestCandidatesOrderByModelFamily(t *testing.T) {
	keyedRelay(t, "k1") // unrestricted, current
	must(t, AddKey("relay", "ant", "ka", Anthropic))
	must(t, AddKey("relay", "oai", "ko", Chat))
	p := mustFind(t)

	keysIn := func(cs []Provider) string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Key)
		}
		return strings.Join(out, ",")
	}

	// Claude models prefer the Anthropic key; the OpenAI-limited key is last
	if got := keysIn(Candidates(p, Anthropic, "claude-sonnet-5")); got != "ka,k1,ko" {
		t.Errorf("claude from anthropic: %s", got)
	}
	if got := keysIn(Candidates(p, Chat, "anthropic/claude-opus")); got != "ka,k1,ko" {
		t.Errorf("claude with vendor prefix: %s", got)
	}
	// GPT, o-series and codex models prefer OpenAI-protocol keys
	for _, m := range []string{"gpt-5.5", "o3-mini", "gpt-5-codex"} {
		if got := keysIn(Candidates(p, Anthropic, m)); got != "ko,k1,ka" {
			t.Errorf("%s from anthropic: %s", m, got)
		}
	}
	// other models prefer keys that speak the client's protocol directly
	if got := keysIn(Candidates(p, Chat, "deepseek-chat")); got != "k1,ko,ka" {
		t.Errorf("other from chat: %s", got)
	}
	if got := keysIn(Candidates(p, Anthropic, "deepseek-chat")); got != "k1,ka,ko" {
		t.Errorf("other from anthropic: %s", got)
	}
}

func TestCandidatesSkipDisabledAndUnusableKeys(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, AddKey("relay", "off", "koff", ""))
	must(t, SetKeyOn("relay", KeyRef("koff"), false))
	must(t, AddKey("relay", "bogus", "kbad", ""))
	// a key limited to a protocol the vendor lacks is never a candidate
	p := mustFind(t)
	p.Chat = ""
	p.Keys[1].Protocol = Responses // vendor has no responses base
	for _, c := range Candidates(p, Chat, "m") {
		if c.Key == "koff" || c.Key == "kbad" {
			t.Errorf("unusable key tried: %s", c.Key)
		}
	}
	if got := Candidates(p, Chat, "m"); len(got) != 1 || got[0].Key != "k1" {
		t.Errorf("candidates: %+v", got)
	}
}

func TestRestrictedKeyUsesOnlyItsEndpoint(t *testing.T) {
	keyedRelay(t, "k1")
	must(t, AddKey("relay", "oai", "ko", Chat))
	p := mustFind(t)
	for _, c := range Candidates(p, Anthropic, "claude") {
		if c.Key == "ko" {
			if eps := c.Endpoints(); len(eps) != 1 || eps[0] != Chat {
				t.Errorf("chat-limited key endpoints: %v", eps)
			}
			if c.Serves(Anthropic) {
				t.Error("chat-limited key serves anthropic")
			}
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
