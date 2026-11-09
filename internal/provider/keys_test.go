package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

// keyxIsolate points magpie's config and every agent path at a fresh temp dir.
func keyxIsolate(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

// keyxProvider saves a custom provider whose current key is key.
func keyxProvider(t *testing.T, key string) {
	t.Helper()
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: key, Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
}

// keyxStored reads the provider back as the file holds it.
func keyxStored(t *testing.T) Provider {
	t.Helper()
	for _, p := range load().Providers {
		if p.ID == "relay" {
			return p
		}
	}
	t.Fatal("relay not stored")
	return Provider{}
}

func keyxMustFail(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: want an error", what)
	}
}

func TestKeyxFingerprintIsTenHexOfSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("sk-abc"))
	want := hex.EncodeToString(sum[:5])
	if got := KeyRef("sk-abc"); got != want || len(got) != 10 || got != strings.ToLower(got) {
		t.Fatalf("KeyRef = %q, want %q", got, want)
	}
}

func TestKeyxAddKeyRules(t *testing.T) {
	keyxIsolate(t)
	keyxProvider(t, "k1")

	keyxMustFail(t, "duplicate of the current key", AddKey("relay", "again", "k1", ""))
	keyxMustFail(t, "unknown protocol", AddKey("relay", "bad", "k2", "gemini"))
	keyxMustFail(t, "unknown protocol word", AddKey("relay", "bad", "k2", Protocol("grpc")))
	if err := AddKey("relay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	keyxMustFail(t, "duplicate of an extra key", AddKey("relay", "team2", "k2", ""))

	p := keyxStored(t)
	if p.Key != "k1" || len(p.Keys) != 1 || p.Keys[0].Key != "k2" || p.Keys[0].Off || p.Keys[0].Protocol != Anthropic || p.Keys[0].Name != "team" {
		t.Fatalf("stored keys: %+v", p)
	}
}

func TestKeyxAddKeyToKeylessProviderBecomesFirst(t *testing.T) {
	keyxIsolate(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Preset: ""}); err == nil {
		t.Fatal("a custom provider with no key should be refused")
	}
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "first", "k1", ""); err != nil {
		t.Fatal(err)
	}
	p, err := Find("local")
	if err != nil {
		t.Fatal(err)
	}
	if p.Key != "k1" || p.KeyName != "first" || len(p.Keys) != 0 {
		t.Fatalf("first key: %+v", p)
	}
}

func TestKeyxLastEnabledKeyStays(t *testing.T) {
	keyxIsolate(t)
	keyxProvider(t, "k1")

	keyxMustFail(t, "only key cannot go off", SetKeyOn("relay", KeyRef("k1"), false))
	keyxMustFail(t, "only key cannot be removed", RemoveKey("relay", KeyRef("k1")))
	if p := keyxStored(t); p.Key != "k1" || len(p.Keys) != 0 {
		t.Fatalf("a refused change still edited the file: %+v", p)
	}

	if err := AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	// turning off the current key hands it to the next enabled one
	if err := SetKeyOn("relay", KeyRef("k1"), false); err != nil {
		t.Fatal(err)
	}
	p := keyxStored(t)
	if p.Key != "k2" || len(p.Keys) != 1 || p.Keys[0].Key != "k1" || !p.Keys[0].Off {
		t.Fatalf("after switching off k1: %+v", p)
	}
	// k2 is now the only enabled key
	keyxMustFail(t, "k2 is the last enabled key", SetKeyOn("relay", KeyRef("k2"), false))
	keyxMustFail(t, "k2 is the last enabled key", RemoveKey("relay", KeyRef("k2")))
	// the disabled key can be removed; k2 stays
	if err := RemoveKey("relay", KeyRef("k1")); err != nil {
		t.Fatal(err)
	}
	if p := keyxStored(t); p.Key != "k2" || len(p.Keys) != 0 {
		t.Fatalf("after removing k1: %+v", p)
	}
}

func TestKeyxSetKeyOnExtraAndRemoveCurrent(t *testing.T) {
	keyxIsolate(t)
	keyxProvider(t, "k1")
	if err := AddKey("relay", "b", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "c", "k3", ""); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", KeyRef("k2"), false); err != nil {
		t.Fatal(err)
	}
	p := keyxStored(t)
	if p.Key != "k1" || !p.Keys[0].Off || p.Keys[1].Off {
		t.Fatalf("extra key off: %+v", p)
	}
	// removing the current key hands over to the next enabled key, skipping the off one
	if err := RemoveKey("relay", KeyRef("k1")); err != nil {
		t.Fatal(err)
	}
	p = keyxStored(t)
	if p.Key != "k3" || p.KeyName != "c" || len(p.Keys) != 1 || p.Keys[0].Key != "k2" || !p.Keys[0].Off {
		t.Fatalf("after removing current: %+v", p)
	}
}

func TestKeyxUseKeyKeepsOthers(t *testing.T) {
	keyxIsolate(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", KeyName: "personal", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "third", "k3", Chat); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", KeyRef("k3")); err != nil {
		t.Fatal(err)
	}
	p := keyxStored(t)
	if p.Key != "k3" || p.KeyName != "third" || p.KeyProtocol != Chat {
		t.Fatalf("current after UseKey: %+v", p)
	}
	if len(p.Keys) != 2 {
		t.Fatalf("extras: %+v", p.Keys)
	}
	if p.Keys[0].Key != "k1" || p.Keys[0].Name != "personal" || p.Keys[0].Off {
		t.Fatalf("old current should be first, enabled, named: %+v", p.Keys[0])
	}
	if p.Keys[1].Key != "k2" || p.Keys[1].Name != "team" || p.Keys[1].Protocol != Anthropic {
		t.Fatalf("team key lost its label or limit: %+v", p.Keys[1])
	}
	// UseKey on the current key changes nothing
	if err := UseKey("relay", KeyRef("k3")); err != nil {
		t.Fatal(err)
	}
	if q := keyxStored(t); q.Key != "k3" || len(q.Keys) != 2 {
		t.Fatalf("using the current key again: %+v", q)
	}
	keyxMustFail(t, "unknown key", UseKey("relay", "0000000000"))
}

func TestKeyxRenameAndProtocol(t *testing.T) {
	keyxIsolate(t)
	keyxProvider(t, "k1")
	if err := AddKey("relay", "team", "k2", ""); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", KeyRef("k2"), "  renamed  "); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", KeyRef("k1"), "main"); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("k2"), Responses); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", KeyRef("k1"), Anthropic); err != nil {
		t.Fatal(err)
	}
	keyxMustFail(t, "bad protocol", SetKeyProtocol("relay", KeyRef("k1"), "gemini"))
	keyxMustFail(t, "unknown key", RenameKey("relay", "0000000000", "x"))

	p := keyxStored(t)
	if p.KeyName != "main" || p.KeyProtocol != Anthropic {
		t.Fatalf("current key: %+v", p)
	}
	if p.Keys[0].Name != "renamed" || p.Keys[0].Protocol != Responses {
		t.Fatalf("extra key: %+v", p.Keys[0])
	}
	if err := SetKeyProtocol("relay", KeyRef("k2"), ""); err != nil {
		t.Fatal(err)
	}
	if p := keyxStored(t); p.Keys[0].Protocol != "" {
		t.Fatalf("protocol not cleared: %+v", p.Keys[0])
	}
}

func TestKeyxSaveKeepsExtraKeys(t *testing.T) {
	keyxIsolate(t)
	keyxProvider(t, "k1")
	if err := AddKey("relay", "team", "k2", Anthropic); err != nil {
		t.Fatal(err)
	}
	// a save that names the same current key must not drop the extras
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if p := keyxStored(t); len(p.Keys) != 1 || p.Keys[0].Key != "k2" || p.Keys[0].Name != "team" {
		t.Fatalf("extras lost on save: %+v", p)
	}
	// a save with a new current key keeps the old one as an extra
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k9", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	p := keyxStored(t)
	if p.Key != "k9" || len(p.Keys) != 2 || p.Keys[0].Key != "k1" || p.Keys[1].Key != "k2" {
		t.Fatalf("after replacing the current key: %+v", p)
	}
}

func TestKeyxSaveRejectsBadKeyFields(t *testing.T) {
	keyxIsolate(t)
	base := Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "https://relay.example.com/v1"}
	bad := base
	bad.KeyProtocol = "gemini"
	keyxMustFail(t, "keyProtocol gemini", Save(bad))
	bad = base
	bad.Keys = []ExtraKey{{Key: "k2", Protocol: "openai"}}
	keyxMustFail(t, "extra protocol openai", Save(bad))
	bad = base
	bad.Keys = []ExtraKey{{Key: "k1"}}
	keyxMustFail(t, "same key twice", Save(bad))
}

func TestKeyxCredentialsOrderAndOff(t *testing.T) {
	p := Provider{ID: "relay", Key: "k1", KeyName: "a", Keys: []ExtraKey{
		{Key: "k2", Name: "b", Off: true},
		{Key: "k3", Name: "c", Protocol: Chat},
	}}
	cs := p.Credentials()
	if len(cs) != 2 || cs[0].Key != "k1" || cs[1].Key != "k3" || cs[1].Protocol != Chat || cs[1].Ref != KeyRef("k3") {
		t.Fatalf("credentials: %+v", cs)
	}
	keyless := Provider{ID: "local", Chat: "http://localhost/v1"}
	if cs := keyless.Credentials(); len(cs) != 1 || cs[0].Key != "" {
		t.Fatalf("keyless provider: %+v", cs)
	}
}
