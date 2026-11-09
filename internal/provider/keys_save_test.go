package provider

import (
	"strings"
	"testing"
)

// TestMultikeySaveKeepsKeys: a save that says nothing about the other keys
// (only ID, Key, Name and a base URL) keeps the team key and the front name.
func TestMultikeySaveKeepsKeys(t *testing.T) {
	multikeyHome(t)
	const first = "sk-first-aaaaaaaa"
	const team = "sk-team-bbbbbbbb"
	multikeyLegacy(t, first)
	if err := AddKey("relay", "团队", team); err != nil {
		t.Fatal(err)
	}

	if err := Save(Provider{ID: "relay", Name: "Relay", Key: first, Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	p := multikeyFind(t)
	if got := multikeyOrder(p); strings.Join(got, ",") != "(on),团队(on)" {
		t.Fatalf("after a plain Save: %v", got)
	}
	if p.Keys[0].Key != team || p.Keys[0].Name != "团队" {
		t.Fatalf("team key lost or renamed: %+v", p.Keys)
	}
}

// TestMultikeySaveNamedFrontKeepsName: when the front key stays and the caller
// gives it a name, the caller's name is used and the extra key is kept.
func TestMultikeySaveNamedFrontKeepsName(t *testing.T) {
	multikeyHome(t)
	const first = "sk-first-aaaaaaaa"
	multikeyLegacy(t, first)
	if err := AddKey("relay", "团队", "sk-team-bbbbbbbb"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: first, KeyName: "个人", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if got := multikeyOrder(multikeyFind(t)); strings.Join(got, ",") != "个人(on),团队(on)" {
		t.Fatalf("after a named Save: %v", got)
	}
}

// TestMultikeySaveDedupesFrontKey: saving a key that is already an extra makes
// it the front key once, taking its name, with no duplicate left behind.
func TestMultikeySaveDedupesFrontKey(t *testing.T) {
	multikeyHome(t)
	const first = "sk-first-aaaaaaaa"
	const team = "sk-team-bbbbbbbb"
	multikeyLegacy(t, first)
	if err := AddKey("relay", "团队", team); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: team, Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	p := multikeyFind(t)
	if p.Key != team || p.KeyName != "团队" {
		t.Fatalf("front after dedupe: key=%q name=%q", p.Key, p.KeyName)
	}
	if got := multikeyOrder(p); len(got) != 1 {
		t.Fatalf("duplicate left behind: %v", got)
	}
}

// TestMultikeySaveNewFrontKeyDropsOldName: replacing the front key with a
// different one does not give the new key the old key's name.
func TestMultikeySaveNewFrontKeyDropsOldName(t *testing.T) {
	multikeyHome(t)
	const first = "sk-first-aaaaaaaa"
	multikeyLegacy(t, first)
	if err := RenameKey("relay", multikeyID(t, "relay"), "个人"); err != nil {
		t.Fatal(err)
	}
	if err := AddKey("relay", "团队", "sk-team-bbbbbbbb"); err != nil {
		t.Fatal(err)
	}
	p := multikeyFind(t)
	p.Key = "sk-new-cccccccc"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got := multikeyFind(t)
	if got.Key != "sk-new-cccccccc" || got.KeyName != "" {
		t.Fatalf("new front key: key=%q name=%q", got.Key, got.KeyName)
	}
	// the replaced front key is gone; the team key keeps its name
	if len(got.Keys) != 1 || got.Keys[0].Name != "团队" || got.Keys[0].Key != "sk-team-bbbbbbbb" {
		t.Fatalf("extras after replacing the front: %+v", got.Keys)
	}
}
