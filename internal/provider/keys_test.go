package provider

import (
	"strings"
	"testing"
)

func TestSeveralKeys(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "Team", "sk-team-2222", ""); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "", "sk-team-2222", ""); err == nil {
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

func TestKeyProtocols(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-one-1111"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "", "sk-bad-3333", "carrier-pigeon"); err == nil || !strings.Contains(err.Error(), `unknown protocol "carrier-pigeon"`) {
		t.Fatalf("err %v", err)
	}
	if p, _ := Find("relay"); len(p.Keys) != 0 {
		t.Fatalf("saved %+v", p.Keys)
	}
	if err := AddKey("relay", "A", "sk-anth-2222", Anthropic); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	if ks := p.KeyList(); ks[0].Protocol != "" || ks[1].Protocol != Anthropic {
		t.Fatalf("%+v", ks)
	}
	if err := SetKeyProtocol("relay", keyID("sk-one-1111"), Chat); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyProtocol("relay", "nope", Chat); err == nil || !strings.Contains(err.Error(), "has no such key") {
		t.Fatalf("err %v", err)
	}
	if err := SetKeyProtocol("relay", keyID("sk-one-1111"), "x"); err == nil {
		t.Fatal("bad protocol accepted")
	}
	p, _ = Find("relay")
	if p.KeyList()[0].Protocol != Chat || p.KeysOn()[0].Protocol != Chat {
		t.Fatalf("%+v", p.KeyList())
	}
	if err := UseKey("relay", keyID("sk-anth-2222")); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-anth-2222" || p.KeyProtocol != Anthropic || p.Keys[0].Protocol != Chat {
		t.Fatalf("after use: %+v", p)
	}
	// off on the first: the next takes over with its own protocol
	if err := SetKeyOn("relay", keyID("sk-anth-2222"), false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-one-1111" || p.KeyProtocol != Chat || p.Keys[0].Protocol != Anthropic || !p.Keys[0].Off {
		t.Fatalf("after off: %+v", p)
	}
}

func TestWithKey(t *testing.T) {
	p := Provider{Chat: "c", Responses: "r", Anthropic: "a", Key: "old"}
	q := p.WithKey(KeyAccount{Name: "n", Key: "new", Protocol: Chat})
	if q.Key != "new" || q.KeyName != "n" || q.KeyProtocol != Chat || q.Chat != "c" || q.Responses != "" || q.Anthropic != "" {
		t.Fatalf("%+v", q)
	}
	if q = p.WithKey(KeyAccount{Key: "new"}); q.Chat == "" || q.Anthropic == "" || q.Responses == "" {
		t.Fatalf("%+v", q)
	}
	if p.Anthropic != "a" {
		t.Fatal("original changed")
	}
}
