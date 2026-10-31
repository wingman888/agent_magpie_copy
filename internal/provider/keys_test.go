package provider

import "testing"

func TestSeveralKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Team", "sk-team-2222"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "", "sk-team-2222"); err == nil {
		t.Fatal("the same key was added twice")
	}
	p, _ := Find("relay")
	ks := p.KeyList()
	if p.Key != "sk-personal-1111" || len(ks) != 2 || !ks[0].Active || ks[1].Name != "Team" {
		t.Fatalf("keys %+v", ks)
	}
	if !ks[1].On || len(p.KeysOn()) != 2 {
		t.Fatalf("a new key is on: %+v", ks)
	}
	if err := RenameKey("relay", ks[0].ID, "Personal"); err != nil {
		t.Fatal(err)
	}
	// first in line, the other stays on behind it
	if err := UseKey("relay", ks[1].ID); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-team-2222" || p.KeyName != "Team" || len(p.Keys) != 1 || p.Keys[0] != (KeyAccount{Name: "Personal", Key: "sk-personal-1111"}) {
		t.Fatalf("after use: %+v %q %+v", p.Key, p.KeyName, p.Keys)
	}
	// off: the next one on takes the lead; the last one on stays
	if err := SetKeyOn("relay", keyID("sk-team-2222"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-personal-1111" || len(p.KeysOn()) != 1 || !p.Keys[0].Off || p.Keys[0].Name != "Team" {
		t.Fatalf("after off: %+v", p)
	}
	if err := SetKeyOn("relay", keyID("sk-personal-1111"), false); err == nil {
		t.Fatal("turned off the last key in use")
	}
	if err := RemoveKey("relay", keyID("sk-personal-1111")); err == nil {
		t.Fatal("removed the last key in use")
	}
	if err := SetKeyOn("relay", keyID("sk-team-2222"), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID("sk-personal-1111")); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); len(p.Keys) != 0 || p.Key != "sk-team-2222" || p.KeyName != "Team" {
		t.Fatalf("after remove: %+v", p)
	}
}
