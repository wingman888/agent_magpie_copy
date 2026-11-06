package provider

import (
	"strings"
	"testing"
)

// keyedRelay saves a custom relay with one key and returns its id.
func keyedRelay(t *testing.T, key string) string {
	t.Helper()
	isolateConfig(t)
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example/v1", Anthropic: "https://relay.example", Key: key}); err != nil {
		t.Fatal(err)
	}
	return "relay"
}

func mustFind(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// names lists the key names in order, current first; "-" marks an off key.
func names(p Provider) string {
	var out []string
	for _, k := range p.ListKeys() {
		n := k.Name
		if !k.On {
			n += "-"
		}
		out = append(out, n)
	}
	return strings.Join(out, ",")
}

func refOf(key string) string { return KeyRef(key) }

func TestKeyRefIsTenHexChars(t *testing.T) {
	r := KeyRef("sk-abc")
	if len(r) != 10 || strings.ToLower(r) != r || strings.Trim(r, "0123456789abcdef") != "" {
		t.Fatalf("ref %q is not 10 lowercase hex chars", r)
	}
	if KeyRef("sk-abc") != r || KeyRef("sk-abd") == r {
		t.Fatalf("ref is not a stable fingerprint")
	}
}

func TestFirstKeyIsCurrentAndAddedKeysFollow(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{Name: "Local", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "first", "sk-1", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "second", "sk-2", ProtoAnthropic); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, "local")
	if p.Key != "sk-1" || p.KeyName != "first" {
		t.Fatalf("current key = %q %q, want sk-1 first", p.Key, p.KeyName)
	}
	if got := names(p); got != "first,second" {
		t.Fatalf("order = %s", got)
	}
	if ks := p.ListKeys(); !ks[0].Current || ks[1].Current || !ks[1].On || ks[1].Protocol != Anthropic {
		t.Fatalf("list = %+v", ks)
	}
}

func TestAddKeyRejectsDuplicatesUnknownProtocolAndAccounts(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	if err := AddKey(id, "again", "sk-1", ""); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := AddKey(id, "x", "sk-2", Protocol("gemini")); err == nil {
		t.Fatal("gemini accepted as a key protocol")
	}
	if err := AddKey(id, "x", "sk-2", Protocol("nonsense")); err == nil {
		t.Fatal("unknown protocol accepted")
	}
	if err := AddKey(id, "x", "  ", ""); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := AddKey("nope", "x", "sk-3", ""); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if p := mustFind(t, id); len(p.ListKeys()) != 1 {
		t.Fatalf("failed adds changed the keys: %s", names(p))
	}
}

func TestSaveRejectsBadStoredProtocol(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{Name: "Bad", Chat: "https://bad.example/v1", Key: "sk-1", KeyProtocol: "gemini"}); err == nil {
		t.Fatal("bad keyProtocol saved")
	}
	if err := Save(Provider{Name: "Bad", Chat: "https://bad.example/v1", Key: "sk-1",
		Keys: []KeyEntry{{Name: "x", Key: "sk-2", Protocol: "foo"}}}); err == nil {
		t.Fatal("bad key protocol saved")
	}
}

func TestUseKeyKeepsOthersAndLimits(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(AddKey(id, "b", "sk-2", ProtoChat))
	must(AddKey(id, "c", "sk-3", ProtoAnthropic))
	must(RenameKey(id, refOf("sk-1"), "a"))
	must(UseKey(id, refOf("sk-3")))
	p := mustFind(t, id)
	if p.Key != "sk-3" || p.KeyProtocol != Anthropic || p.KeyName != "c" {
		t.Fatalf("current = %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	if got := names(p); got != "c,a,b" {
		t.Fatalf("order after use = %s", got)
	}
	// the limit of b survives too
	for _, k := range p.Keys {
		if k.Name == "b" && k.Protocol != Chat {
			t.Fatalf("b lost its limit: %+v", k)
		}
	}
	if err := UseKey(id, "0000000000"); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestDisableCurrentHandsOverToNext(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	if err := AddKey(id, "b", "sk-2", Chat); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn(id, refOf("sk-1"), false); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, id)
	if p.Key != "sk-2" || p.KeyName != "b" || p.KeyProtocol != Chat {
		t.Fatalf("next key did not take over: %q %q %q", p.Key, p.KeyName, p.KeyProtocol)
	}
	// the first key had no name; it is kept, off, after the current one
	if got := names(p); got != "b,-" {
		t.Fatalf("order = %s", got)
	}
	creds := p.Credentials()
	if len(creds) != 1 || creds[0].Key != "sk-2" {
		t.Fatalf("credentials = %+v", creds)
	}
	// turning it back on keeps it after the current key
	if err := SetKeyOn(id, refOf("sk-1"), true); err != nil {
		t.Fatal(err)
	}
	if got := len(mustFind(t, id).Credentials()); got != 2 {
		t.Fatalf("re-enabled key not tried: %d credentials", got)
	}
}

func TestLastEnabledKeyCannotBeDisabledOrRemoved(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	if err := SetKeyOn(id, refOf("sk-1"), false); err == nil {
		t.Fatal("the only key was disabled")
	}
	if err := RemoveKey(id, refOf("sk-1")); err == nil {
		t.Fatal("the only key was removed")
	}
	if err := AddKey(id, "b", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn(id, refOf("sk-2"), false); err != nil {
		t.Fatal(err)
	}
	// sk-1 is now the only enabled key
	if err := SetKeyOn(id, refOf("sk-1"), false); err == nil {
		t.Fatal("the last enabled key was disabled")
	}
	if err := RemoveKey(id, refOf("sk-1")); err == nil {
		t.Fatal("the last enabled key was removed")
	}
	p := mustFind(t, id)
	if p.Key != "sk-1" || len(p.Credentials()) != 1 {
		t.Fatalf("refused changes still changed the list: %s", names(p))
	}
}

func TestRemoveCurrentPromotesNextEnabled(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(AddKey(id, "b", "sk-2", ""))
	must(AddKey(id, "c", "sk-3", ""))
	must(RemoveKey(id, refOf("sk-1")))
	p := mustFind(t, id)
	if p.Key != "sk-2" || p.KeyName != "b" {
		t.Fatalf("current after remove = %q %q", p.Key, p.KeyName)
	}
	must(RemoveKey(id, refOf("sk-3")))
	if got := names(mustFind(t, id)); got != "b" {
		t.Fatalf("list = %s", got)
	}
}

func TestRenameAndProtocolOfAnyKey(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	if err := AddKey(id, "b", "sk-2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey(id, refOf("sk-2"), "team"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey(id, refOf("sk-1"), "personal"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol(id, refOf("sk-2"), Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol(id, refOf("sk-1"), Protocol("bogus")); err == nil {
		t.Fatal("bogus protocol accepted")
	}
	p := mustFind(t, id)
	if p.KeyName != "personal" || p.Keys[0].Name != "team" || p.Keys[0].Protocol != Anthropic {
		t.Fatalf("after rename/protocol: %q %+v", p.KeyName, p.Keys)
	}
	if err := SetKeyProtocol(id, refOf("sk-2"), ""); err != nil {
		t.Fatal(err)
	}
	if p := mustFind(t, id); p.Keys[0].Protocol != "" {
		t.Fatalf("protocol not cleared: %+v", p.Keys[0])
	}
	if err := RenameKey(id, "0000000000", "x"); err == nil {
		t.Fatal("unknown ref renamed")
	}
	if err := SetKeyProtocol("nope", refOf("sk-1"), ""); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestCredentialsSkipOffKeysInOrder(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	if err := AddKey(id, "b", "sk-2", ProtoResponses); err != nil {
		t.Fatal(err)
	}
	if err := AddKey(id, "c", "sk-3", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn(id, refOf("sk-2"), false); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range mustFind(t, id).Credentials() {
		got = append(got, c.Key)
	}
	if strings.Join(got, ",") != "sk-1,sk-3" {
		t.Fatalf("credentials = %v", got)
	}
}

func TestKeylessProviderHasOneEmptyCredential(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{Name: "Local", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	creds := mustFind(t, "local").Credentials()
	if len(creds) != 1 || creds[0].Key != "" {
		t.Fatalf("credentials = %+v", creds)
	}
}

func TestKeysSurviveSaveAndReload(t *testing.T) {
	id := keyedRelay(t, "sk-1")
	if err := AddKey(id, "team", "sk-2", ProtoAnthropic); err != nil {
		t.Fatal(err)
	}
	p := mustFind(t, id)
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	// sk-1 has no name, so its entry is blank
	if got := names(mustFind(t, id)); got != ",team" {
		t.Fatalf("after save: %s", got)
	}
}
