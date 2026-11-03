package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	keyA = "sk-personal-0001" // front key in most tests
	keyB = "sk-team-0002"
	keyC = "sk-free-0003"
)

// keyHome points config and home at a temp dir, away from the machine's own logins.
func keyHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// legacy writes a provider as the old config had it: one key, no names, no keys.
func legacy(t *testing.T, key string) {
	t.Helper()
	keyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "key": key, "chat": "https://relay.example/v1",
	}}})
}

func get(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// state lists each key as "id/active/on" in attempt order.
func state(p Provider) []string {
	var out []string
	for _, k := range p.KeyList() {
		out = append(out, fmt.Sprintf("%s/%v/%v", k.ID, k.Active, k.On))
	}
	return out
}

func want(keys ...string) []string {
	var out []string
	for i, k := range keys {
		out = append(out, fmt.Sprintf("%s/%v/%v", keyID(k), i == 0, true))
	}
	return out
}

// three builds relay with keyA in front, then keyB and keyC after it, all on.
func three(t *testing.T) {
	t.Helper()
	legacy(t, keyA)
	for _, k := range []string{keyB, keyC} {
		if err := AddKey("relay", "", k); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLegacySingleKeyIsUnchanged(t *testing.T) {
	legacy(t, keyA)
	p := get(t, "relay")
	if !slices.Equal(state(p), want(keyA)) {
		t.Fatalf("legacy key list: %v", state(p))
	}
	if ks := p.KeysOn(); len(ks) != 1 || ks[0].Key != keyA {
		t.Fatalf("legacy KeysOn: %+v", ks)
	}
	// saving it again writes the old shape: no keyName, no keys
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "keyName") || strings.Contains(string(b), `"keys"`) {
		t.Fatalf("legacy save grew new fields:\n%s", b)
	}
}

func TestAddKey(t *testing.T) {
	legacy(t, keyA)
	if err := AddKey("relay", "  团队  ", "  "+keyB+"\n"); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	if !slices.Equal(state(p), want(keyA, keyB)) {
		t.Fatalf("after AddKey: %v", state(p))
	}
	if kl := p.KeyList(); kl[1].Name != "团队" {
		t.Fatalf("name not trimmed: %q", kl[1].Name)
	}
	if err := AddKey("relay", "again", keyB); err == nil {
		t.Fatal("duplicate key accepted")
	} else if strings.Contains(err.Error(), keyB) {
		t.Fatalf("duplicate error leaks the key: %v", err)
	}
	if err := AddKey("relay", "", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := AddKey("nope", "", keyC); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if !slices.Equal(state(get(t, "relay")), want(keyA, keyB)) {
		t.Fatal("failed AddKey changed the keys")
	}
}

func TestAddKeyToKeylessProviderMakesItFront(t *testing.T) {
	keyHome(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if len(get(t, "local").KeyList()) != 0 {
		t.Fatal("keyless provider reports keys")
	}
	if err := AddKey("local", "first", keyA); err != nil {
		t.Fatal(err)
	}
	p := get(t, "local")
	if !slices.Equal(state(p), want(keyA)) || p.Key != keyA || p.KeyName != "first" {
		t.Fatalf("first key: %+v", p)
	}
}

func TestUseKey(t *testing.T) {
	three(t)
	if err := UseKey("relay", keyID(keyC)); err != nil {
		t.Fatal(err)
	}
	if got := state(get(t, "relay")); !slices.Equal(got, want(keyC, keyA, keyB)) {
		t.Fatalf("UseKey C: %v", got)
	}
	// the front key again: no change
	if err := UseKey("relay", keyID(keyC)); err != nil {
		t.Fatal(err)
	}
	if got := state(get(t, "relay")); !slices.Equal(got, want(keyC, keyA, keyB)) {
		t.Fatalf("UseKey on front changed order: %v", got)
	}
	if err := UseKey("relay", "deadbeef0000"); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestUseKeyTurnsOnAndKeepsOldFrontOn(t *testing.T) {
	three(t)
	if err := SetKeyOn("relay", keyID(keyB), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", keyID(keyB)); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	if !slices.Equal(state(p), want(keyB, keyA, keyC)) {
		t.Fatalf("UseKey on off key: %v", state(p))
	}
}

func TestSetKeyOn(t *testing.T) {
	three(t)
	// a key that is not front can be turned off; the front stays
	if err := SetKeyOn("relay", keyID(keyC), false); err != nil {
		t.Fatal(err)
	}
	if got := state(get(t, "relay")); !slices.Equal(got, []string{
		keyID(keyA) + "/true/true", keyID(keyB) + "/false/true", keyID(keyC) + "/false/false",
	}) {
		t.Fatalf("off non-front: %v", got)
	}
	// turning off the front hands the front to the next key that is on
	if err := SetKeyOn("relay", keyID(keyA), false); err != nil {
		t.Fatal(err)
	}
	if got := state(get(t, "relay")); !slices.Equal(got, []string{
		keyID(keyB) + "/true/true", keyID(keyA) + "/false/false", keyID(keyC) + "/false/false",
	}) {
		t.Fatalf("off front: %v", got)
	}
	// the last key that is on cannot be turned off, and nothing changes
	before := state(get(t, "relay"))
	if err := SetKeyOn("relay", keyID(keyB), false); err == nil {
		t.Fatal("turned off the last key on")
	} else if strings.Contains(err.Error(), keyB) {
		t.Fatalf("error leaks the key: %v", err)
	}
	if !slices.Equal(state(get(t, "relay")), before) {
		t.Fatal("refused SetKeyOn changed the keys")
	}
	// turning a key back on keeps the front where it is
	if err := SetKeyOn("relay", keyID(keyA), true); err != nil {
		t.Fatal(err)
	}
	if got := state(get(t, "relay")); got[0] != keyID(keyB)+"/true/true" || got[1] != keyID(keyA)+"/false/true" {
		t.Fatalf("on again: %v", got)
	}
	if err := SetKeyOn("relay", "deadbeef0000", true); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestSetKeyOnFrontNeedsAnotherOn(t *testing.T) {
	legacy(t, keyA)
	if err := SetKeyOn("relay", keyID(keyA), false); err == nil {
		t.Fatal("turned off the only key")
	}
	if !slices.Equal(state(get(t, "relay")), want(keyA)) {
		t.Fatal("single key changed")
	}
}

func TestRemoveKey(t *testing.T) {
	three(t)
	// removing a key that is on, when it is the only one on, is refused
	if err := SetKeyOn("relay", keyID(keyB), false); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", keyID(keyC), false); err != nil {
		t.Fatal(err)
	}
	before := state(get(t, "relay"))
	if err := RemoveKey("relay", keyID(keyA)); err == nil {
		t.Fatal("removed the last key on")
	} else if strings.Contains(err.Error(), keyA) {
		t.Fatalf("error leaks the key: %v", err)
	}
	if !slices.Equal(state(get(t, "relay")), before) {
		t.Fatal("refused RemoveKey changed the keys")
	}
	// bring B back, then remove the front: B takes the front
	if err := SetKeyOn("relay", keyID(keyB), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID(keyA)); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	if !slices.Equal(state(p), []string{keyID(keyB) + "/true/true", keyID(keyC) + "/false/false"}) {
		t.Fatalf("after removing front: %v", state(p))
	}
	// a key that is off can be removed freely
	if err := RemoveKey("relay", keyID(keyC)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(state(get(t, "relay")), []string{keyID(keyB) + "/true/true"}) {
		t.Fatalf("after removing off key: %v", state(get(t, "relay")))
	}
	if err := RemoveKey("relay", keyID(keyC)); err == nil {
		t.Fatal("removed an unknown key")
	}
}

func TestRemoveNonFrontKeepsNames(t *testing.T) {
	three(t)
	if err := RenameKey("relay", keyID(keyB), "team"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", keyID(keyC), "free"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID(keyB)); err != nil {
		t.Fatal(err)
	}
	kl := get(t, "relay").KeyList()
	if len(kl) != 2 || kl[1].Name != "free" || kl[0].Name != "" {
		t.Fatalf("names after removal: %+v", kl)
	}
}

func TestRenameKey(t *testing.T) {
	three(t)
	if err := RenameKey("relay", keyID(keyB), "  团队  "); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", keyID(keyA), "个人"); err != nil {
		t.Fatal(err)
	}
	kl := get(t, "relay").KeyList()
	if kl[0].Name != "个人" || kl[1].Name != "团队" {
		t.Fatalf("names: %+v", kl)
	}
	if err := RenameKey("relay", keyID(keyB), ""); err != nil {
		t.Fatal(err)
	}
	if kl := get(t, "relay").KeyList(); kl[1].Name != "" {
		t.Fatalf("name not cleared: %+v", kl[1])
	}
	if err := RenameKey("relay", "deadbeef0000", "x"); err == nil {
		t.Fatal("renamed an unknown key")
	}
}

func TestKeysOnFollowsAttemptOrder(t *testing.T) {
	three(t)
	if err := SetKeyOn("relay", keyID(keyA), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", keyID(keyC)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range get(t, "relay").KeysOn() {
		got = append(got, k.Key)
	}
	if !slices.Equal(got, []string{keyC, keyB}) {
		t.Fatalf("KeysOn: %v", got)
	}
}

func TestMaskedOutputNeverHasFullKey(t *testing.T) {
	three(t)
	_ = SetKeyOn("relay", keyID(keyA), false)
	_ = RemoveKey("relay", "deadbeef0000")
	_ = AddKey("relay", "dup", keyB)
	p := get(t, "relay")
	out := fmt.Sprint(p.KeyList())
	for _, k := range []string{keyA, keyB, keyC} {
		if strings.Contains(out, k) {
			t.Fatalf("KeyList leaks %q: %s", k, out)
		}
	}
}

func TestSignedInAccountTakesNoKeys(t *testing.T) {
	signIn(t)
	if err := AddKey("codex", "", "sk-account-0009"); err == nil {
		t.Fatal("account accepted an API key")
	} else if strings.Contains(err.Error(), "sk-account-0009") {
		t.Fatalf("error leaks the key: %v", err)
	}
}
