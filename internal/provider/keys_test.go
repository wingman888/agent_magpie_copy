package provider

import "testing"

func keyFixture(t *testing.T) {
	t.Helper()
	signIn(t)
	if err := Save(Provider{ID: "multi", Name: "Multi", Key: "key-b", KeyName: "B", Chat: "https://multi.example/v1",
		Keys: []KeyAccount{{Name: "A", Key: "key-a"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestKeyLifecycle(t *testing.T) {
	keyFixture(t)
	a, b := KeyID("key-a"), KeyID("key-b")
	if err := AddKey("multi", " Team ", " key-t "); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("multi", "dup", "key-a"); err == nil || err.Error() != "Multi already has this key" {
		t.Fatalf("dup: %v", err)
	}
	if err := AddKey("multi", "", "  "); err == nil || err.Error() != "paste the key to add" {
		t.Fatalf("blank: %v", err)
	}
	if err := RemoveKey("multi", KeyID("key-t")); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("multi", b, false); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("multi")
	if p.Key != "key-a" || p.KeyName != "A" || len(p.KeysOn()) != 1 || p.Keys[0].Key != "key-b" || !p.Keys[0].Off || p.Keys[0].Name != "B" {
		t.Fatalf("after off: %+v", p)
	}
	if err := SetKeyOn("multi", a, false); err == nil || err.Error() != "that's the only key in use; turn another on first" {
		t.Fatalf("last off: %v", err)
	}
	if err := RemoveKey("multi", a); err == nil || err.Error() != "that's the only key in use; turn another on before removing it" {
		t.Fatalf("last remove: %v", err)
	}
	if err := SetKeyOn("multi", b, true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("multi", a); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("multi")
	if p.Key != "key-b" || p.KeyName != "B" || len(p.Keys) != 0 {
		t.Fatalf("after remove: %+v", p)
	}
	if err := RenameKey("multi", b, " Bee "); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("multi", "nope", "x"); err == nil {
		t.Fatal("rename of a missing key")
	}
}

func TestUseKeyEnablesAndReorders(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "multi", Name: "Multi", Key: "k1", KeyName: "one", Chat: "https://multi.example/v1",
		Keys: []KeyAccount{{Name: "two", Key: "k2"}, {Name: "three", Key: "k3", Off: true}, {Key: ""}}}); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("multi", KeyID("k3")); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("multi")
	if p.Key != "k3" || p.KeyName != "three" || len(p.Keys) != 2 || p.Keys[0].Key != "k1" || p.Keys[0].Off || p.Keys[1].Key != "k2" {
		t.Fatalf("use: %+v", p)
	}
	if err := UseKey("multi", KeyID("k3")); err != nil {
		t.Fatal(err)
	}
	if len(p.KeysOn()) != 3 {
		t.Fatalf("keys on: %+v", p.KeysOn())
	}
}

func TestAddKeyToEmptyAndAccount(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("local", "First", "k1"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("local"); p.Key != "k1" || p.KeyName != "First" {
		t.Fatalf("%+v", p)
	}
	if err := AddKey("codex", "x", "k"); err == nil || err.Error() != "a signed-in account has no keys" {
		t.Fatalf("account: %v", err)
	}
}
