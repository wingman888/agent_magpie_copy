package provider_test

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func setup(t *testing.T, p provider.Provider) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, id string) provider.Provider {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

func wantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("error %v, want containing %q", err, sub)
	}
}

func TestAcceptKeyIDAndSingleKeyList(t *testing.T) {
	if provider.KeyID("a") != provider.KeyID("a") || provider.KeyID("a") == provider.KeyID("b") {
		t.Fatal("KeyID not a stable distinct fingerprint")
	}
	setup(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-one-1111"})
	p := get(t, "relay")
	ks := p.KeyList()
	if len(ks) != 1 || !ks[0].Active || !ks[0].On || ks[0].ID != provider.KeyID("sk-one-1111") || ks[0].Masked != provider.Mask("sk-one-1111") {
		t.Fatalf("%+v", ks)
	}
}

func TestAcceptAddKey(t *testing.T) {
	setup(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-one-1111"})
	if err := provider.AddKey("relay", "  Team ", "  sk-team-2222 "); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	ks := p.KeyList()
	if p.Key != "sk-one-1111" || len(ks) != 2 || ks[1].Name != "Team" || !ks[1].On || len(p.KeysOn()) != 2 {
		t.Fatalf("%+v", ks)
	}
	if p.KeysOn()[1].Key != "sk-team-2222" {
		t.Fatalf("%+v", p.KeysOn())
	}
	wantErr(t, provider.AddKey("relay", "x", "sk-team-2222"), "Relay already has this key")
	wantErr(t, provider.AddKey("relay", "x", "sk-one-1111"), "already has this key")
	wantErr(t, provider.AddKey("relay", "x", "   "), "paste the key to add")
	if q := get(t, "relay"); len(q.Keys) != 1 || q.Keys[0].Name != "Team" {
		t.Fatalf("changed: %+v", q.Keys)
	}
	wantErr(t, provider.AddKey("nope-missing", "x", "k"), "")
}

func TestAcceptAddKeyToEmptyProvider(t *testing.T) {
	setup(t, provider.Provider{ID: "bare", Name: "Bare", Chat: "http://localhost:1/v1"})
	if err := provider.AddKey("bare", "Main", "sk-main"); err != nil {
		t.Fatal(err)
	}
	p := get(t, "bare")
	if p.Key != "sk-main" || p.KeyName != "Main" || len(p.Keys) != 0 {
		t.Fatalf("%+v", p)
	}
	if len((provider.Provider{}).KeysOn()) != 0 {
		t.Fatal("empty provider has keys on")
	}
}

func TestAcceptRename(t *testing.T) {
	setup(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "k1"})
	if err := provider.AddKey("relay", "", "k2"); err != nil {
		t.Fatal(err)
	}
	if err := provider.RenameKey("relay", provider.KeyID("k1"), " Personal "); err != nil {
		t.Fatal(err)
	}
	if err := provider.RenameKey("relay", provider.KeyID("k2"), "Team"); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	ks := p.KeyList()
	if p.KeyName != "Personal" || ks[0].Name != "Personal" || ks[1].Name != "Team" {
		t.Fatalf("%+v", ks)
	}
	wantErr(t, provider.RenameKey("relay", "bogus", "x"), "has no such key")
}

func TestAcceptUseKey(t *testing.T) {
	setup(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "k1", KeyName: "Personal"})
	provider.AddKey("relay", "Team", "k2")
	provider.AddKey("relay", "Third", "k3")
	if err := provider.UseKey("relay", provider.KeyID("k1")); err != nil {
		t.Fatal(err)
	}
	if p := get(t, "relay"); p.Key != "k1" || len(p.Keys) != 2 {
		t.Fatalf("noop changed: %+v", p)
	}
	if err := provider.UseKey("relay", provider.KeyID("k3")); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	if p.Key != "k3" || p.KeyName != "Third" || len(p.Keys) != 2 ||
		p.Keys[0] != (provider.KeyAccount{Name: "Personal", Key: "k1"}) ||
		p.Keys[1] != (provider.KeyAccount{Name: "Team", Key: "k2"}) {
		t.Fatalf("%+v", p)
	}
	wantErr(t, provider.UseKey("relay", "bogus"), "Relay has no such key")
}

func TestAcceptUseKeyTurnsOnOffAccount(t *testing.T) {
	setup(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "k1",
		Keys: []provider.KeyAccount{{Name: "A", Key: "k2"}, {Name: "Off", Key: "k3", Off: true}, {Name: "Empty", Key: ""}}})
	p := get(t, "relay")
	if len(p.KeysOn()) != 2 {
		t.Fatalf("%+v", p.KeysOn())
	}
	if err := provider.UseKey("relay", provider.KeyID("k3")); err != nil {
		t.Fatal(err)
	}
	p = get(t, "relay")
	if p.Key != "k3" || p.KeyName != "Off" {
		t.Fatalf("%+v", p)
	}
	on := p.KeysOn()
	if len(on) != 3 || on[0].Key != "k3" || on[1].Key != "k1" || on[2].Key != "k2" {
		t.Fatalf("%+v", on)
	}
}

func TestAcceptSetKeyOnAndRemove(t *testing.T) {
	setup(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "kB", KeyName: "B",
		Keys: []provider.KeyAccount{{Name: "A", Key: "kA"}}})
	idA, idB := provider.KeyID("kA"), provider.KeyID("kB")
	if err := provider.SetKeyOn("relay", idB, true); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeyOn("relay", idB, false); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	if p.Key != "kA" || p.KeyName != "A" || len(p.KeysOn()) != 1 || len(p.Keys) == 0 ||
		p.Keys[0] != (provider.KeyAccount{Name: "B", Key: "kB", Off: true}) {
		t.Fatalf("%+v", p)
	}
	wantErr(t, provider.SetKeyOn("relay", idA, false), "that's the only key in use; turn another on first")
	wantErr(t, provider.RemoveKey("relay", idA), "that's the only key in use; turn another on before removing it")
	if q := get(t, "relay"); q.Key != "kA" || len(q.Keys) != 1 || !q.Keys[0].Off {
		t.Fatalf("changed: %+v", q)
	}
	if err := provider.SetKeyOn("relay", idB, true); err != nil {
		t.Fatal(err)
	}
	if err := provider.RemoveKey("relay", idA); err != nil {
		t.Fatal(err)
	}
	p = get(t, "relay")
	if p.Key != "kB" || p.KeyName != "B" || len(p.Keys) != 0 {
		t.Fatalf("%+v", p)
	}
	wantErr(t, provider.SetKeyOn("relay", "bogus", true), "Relay has no such key")
	wantErr(t, provider.RemoveKey("relay", "bogus"), "Relay has no such key")
}

func TestAcceptRemoveOffKey(t *testing.T) {
	setup(t, provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "k1",
		Keys: []provider.KeyAccount{{Name: "Off", Key: "k2", Off: true}}})
	if err := provider.RemoveKey("relay", provider.KeyID("k2")); err != nil {
		t.Fatal(err)
	}
	if p := get(t, "relay"); p.Key != "k1" || len(p.Keys) != 0 {
		t.Fatalf("%+v", p)
	}
}
