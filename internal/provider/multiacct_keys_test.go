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
	multiAcctKeyA = "sk-personal-0001" // front key in most tests
	multiAcctKeyB = "sk-team-0002"
	multiAcctKeyC = "sk-free-0003"
)

// keyHome points config and home at a temp dir, away from the machine's own logins.
func multiAcctKeyHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// legacy writes a provider as the old config had it: one key, no names, no keys.
func multiAcctLegacy(t *testing.T, key string) {
	t.Helper()
	multiAcctKeyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "key": key, "chat": "https://relay.example/v1",
	}}})
}

func multiAcctGet(t *testing.T, id string) Provider {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *p
}

// state lists each key as "id/active/on" in attempt order.
func multiAcctState(p Provider) []string {
	var out []string
	for _, k := range p.KeyList() {
		out = append(out, fmt.Sprintf("%s/%v/%v", k.ID, k.Active, k.On))
	}
	return out
}

func multiAcctWant(keys ...string) []string {
	var out []string
	for i, k := range keys {
		out = append(out, fmt.Sprintf("%s/%v/%v", keyID(k), i == 0, true))
	}
	return out
}

// three builds relay with multiAcctKeyA in front, then multiAcctKeyB and multiAcctKeyC after it, all on.
func multiAcctThree(t *testing.T) {
	t.Helper()
	multiAcctLegacy(t, multiAcctKeyA)
	for _, k := range []string{multiAcctKeyB, multiAcctKeyC} {
		if err := AddKey("relay", "", k); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMultiAcctLegacySingleKeyIsUnchanged(t *testing.T) {
	multiAcctLegacy(t, multiAcctKeyA)
	p := multiAcctGet(t, "relay")
	if !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyA)) {
		t.Fatalf("legacy key list: %v", multiAcctState(p))
	}
	if ks := p.KeysOn(); len(ks) != 1 || ks[0].Key != multiAcctKeyA {
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

func TestMultiAcctAddKey(t *testing.T) {
	multiAcctLegacy(t, multiAcctKeyA)
	if err := AddKey("relay", "  团队  ", "  "+multiAcctKeyB+"\n"); err != nil {
		t.Fatal(err)
	}
	p := multiAcctGet(t, "relay")
	if !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyA, multiAcctKeyB)) {
		t.Fatalf("after AddKey: %v", multiAcctState(p))
	}
	if kl := p.KeyList(); kl[1].Name != "团队" {
		t.Fatalf("name not trimmed: %q", kl[1].Name)
	}
	if err := AddKey("relay", "again", multiAcctKeyB); err == nil {
		t.Fatal("duplicate key accepted")
	} else if strings.Contains(err.Error(), multiAcctKeyB) {
		t.Fatalf("duplicate error leaks the key: %v", err)
	}
	if err := AddKey("relay", "", "   "); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := AddKey("nope", "", multiAcctKeyC); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if !slices.Equal(multiAcctState(multiAcctGet(t, "relay")), multiAcctWant(multiAcctKeyA, multiAcctKeyB)) {
		t.Fatal("failed AddKey changed the keys")
	}
}

func TestMultiAcctAddKeyToKeylessProviderMakesItFront(t *testing.T) {
	multiAcctKeyHome(t)
	if err := Save(Provider{ID: "local", Name: "Local", Chat: "http://localhost:1234/v1"}); err != nil {
		t.Fatal(err)
	}
	if len(multiAcctGet(t, "local").KeyList()) != 0 {
		t.Fatal("keyless provider reports keys")
	}
	if err := AddKey("local", "first", multiAcctKeyA); err != nil {
		t.Fatal(err)
	}
	p := multiAcctGet(t, "local")
	if !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyA)) || p.Key != multiAcctKeyA || p.KeyName != "first" {
		t.Fatalf("first key: %+v", p)
	}
}

func TestMultiAcctUseKey(t *testing.T) {
	multiAcctThree(t)
	if err := UseKey("relay", keyID(multiAcctKeyC)); err != nil {
		t.Fatal(err)
	}
	if got := multiAcctState(multiAcctGet(t, "relay")); !slices.Equal(got, multiAcctWant(multiAcctKeyC, multiAcctKeyA, multiAcctKeyB)) {
		t.Fatalf("UseKey C: %v", got)
	}
	// the front key again: no change
	if err := UseKey("relay", keyID(multiAcctKeyC)); err != nil {
		t.Fatal(err)
	}
	if got := multiAcctState(multiAcctGet(t, "relay")); !slices.Equal(got, multiAcctWant(multiAcctKeyC, multiAcctKeyA, multiAcctKeyB)) {
		t.Fatalf("UseKey on front changed order: %v", got)
	}
	if err := UseKey("relay", "deadbeef0000"); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestMultiAcctUseKeyTurnsOnAndKeepsOldFrontOn(t *testing.T) {
	multiAcctThree(t)
	if err := SetKeyOn("relay", keyID(multiAcctKeyB), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", keyID(multiAcctKeyB)); err != nil {
		t.Fatal(err)
	}
	p := multiAcctGet(t, "relay")
	if !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyB, multiAcctKeyA, multiAcctKeyC)) {
		t.Fatalf("UseKey on off key: %v", multiAcctState(p))
	}
}

func TestMultiAcctSetKeyOn(t *testing.T) {
	multiAcctThree(t)
	// a key that is not front can be turned off; the front stays
	if err := SetKeyOn("relay", keyID(multiAcctKeyC), false); err != nil {
		t.Fatal(err)
	}
	if got := multiAcctState(multiAcctGet(t, "relay")); !slices.Equal(got, []string{
		keyID(multiAcctKeyA) + "/true/true", keyID(multiAcctKeyB) + "/false/true", keyID(multiAcctKeyC) + "/false/false",
	}) {
		t.Fatalf("off non-front: %v", got)
	}
	// turning off the front hands the front to the next key that is on
	if err := SetKeyOn("relay", keyID(multiAcctKeyA), false); err != nil {
		t.Fatal(err)
	}
	if got := multiAcctState(multiAcctGet(t, "relay")); !slices.Equal(got, []string{
		keyID(multiAcctKeyB) + "/true/true", keyID(multiAcctKeyA) + "/false/false", keyID(multiAcctKeyC) + "/false/false",
	}) {
		t.Fatalf("off front: %v", got)
	}
	// the last key that is on cannot be turned off, and nothing changes
	before := multiAcctState(multiAcctGet(t, "relay"))
	if err := SetKeyOn("relay", keyID(multiAcctKeyB), false); err == nil {
		t.Fatal("turned off the last key on")
	} else if strings.Contains(err.Error(), multiAcctKeyB) {
		t.Fatalf("error leaks the key: %v", err)
	}
	if !slices.Equal(multiAcctState(multiAcctGet(t, "relay")), before) {
		t.Fatal("refused SetKeyOn changed the keys")
	}
	// turning a key back on keeps the front where it is
	if err := SetKeyOn("relay", keyID(multiAcctKeyA), true); err != nil {
		t.Fatal(err)
	}
	if got := multiAcctState(multiAcctGet(t, "relay")); got[0] != keyID(multiAcctKeyB)+"/true/true" || got[1] != keyID(multiAcctKeyA)+"/false/true" {
		t.Fatalf("on again: %v", got)
	}
	if err := SetKeyOn("relay", "deadbeef0000", true); err == nil {
		t.Fatal("unknown ref accepted")
	}
}

func TestMultiAcctSetKeyOnFrontNeedsAnotherOn(t *testing.T) {
	multiAcctLegacy(t, multiAcctKeyA)
	if err := SetKeyOn("relay", keyID(multiAcctKeyA), false); err == nil {
		t.Fatal("turned off the only key")
	}
	if !slices.Equal(multiAcctState(multiAcctGet(t, "relay")), multiAcctWant(multiAcctKeyA)) {
		t.Fatal("single key changed")
	}
}

func TestMultiAcctRemoveKey(t *testing.T) {
	multiAcctThree(t)
	// removing a key that is on, when it is the only one on, is refused
	if err := SetKeyOn("relay", keyID(multiAcctKeyB), false); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOn("relay", keyID(multiAcctKeyC), false); err != nil {
		t.Fatal(err)
	}
	before := multiAcctState(multiAcctGet(t, "relay"))
	if err := RemoveKey("relay", keyID(multiAcctKeyA)); err == nil {
		t.Fatal("removed the last key on")
	} else if strings.Contains(err.Error(), multiAcctKeyA) {
		t.Fatalf("error leaks the key: %v", err)
	}
	if !slices.Equal(multiAcctState(multiAcctGet(t, "relay")), before) {
		t.Fatal("refused RemoveKey changed the keys")
	}
	// bring B back, then remove the front: B takes the front
	if err := SetKeyOn("relay", keyID(multiAcctKeyB), true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID(multiAcctKeyA)); err != nil {
		t.Fatal(err)
	}
	p := multiAcctGet(t, "relay")
	if !slices.Equal(multiAcctState(p), []string{keyID(multiAcctKeyB) + "/true/true", keyID(multiAcctKeyC) + "/false/false"}) {
		t.Fatalf("after removing front: %v", multiAcctState(p))
	}
	// a key that is off can be removed freely
	if err := RemoveKey("relay", keyID(multiAcctKeyC)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(multiAcctState(multiAcctGet(t, "relay")), []string{keyID(multiAcctKeyB) + "/true/true"}) {
		t.Fatalf("after removing off key: %v", multiAcctState(multiAcctGet(t, "relay")))
	}
	if err := RemoveKey("relay", keyID(multiAcctKeyC)); err == nil {
		t.Fatal("removed an unknown key")
	}
}

func TestMultiAcctRemoveNonFrontKeepsNames(t *testing.T) {
	multiAcctThree(t)
	if err := RenameKey("relay", keyID(multiAcctKeyB), "team"); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", keyID(multiAcctKeyC), "free"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", keyID(multiAcctKeyB)); err != nil {
		t.Fatal(err)
	}
	kl := multiAcctGet(t, "relay").KeyList()
	if len(kl) != 2 || kl[1].Name != "free" || kl[0].Name != "" {
		t.Fatalf("names after removal: %+v", kl)
	}
}

func TestMultiAcctRenameKey(t *testing.T) {
	multiAcctThree(t)
	if err := RenameKey("relay", keyID(multiAcctKeyB), "  团队  "); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", keyID(multiAcctKeyA), "个人"); err != nil {
		t.Fatal(err)
	}
	kl := multiAcctGet(t, "relay").KeyList()
	if kl[0].Name != "个人" || kl[1].Name != "团队" {
		t.Fatalf("names: %+v", kl)
	}
	if err := RenameKey("relay", keyID(multiAcctKeyB), ""); err != nil {
		t.Fatal(err)
	}
	if kl := multiAcctGet(t, "relay").KeyList(); kl[1].Name != "" {
		t.Fatalf("name not cleared: %+v", kl[1])
	}
	if err := RenameKey("relay", "deadbeef0000", "x"); err == nil {
		t.Fatal("renamed an unknown key")
	}
}

func TestMultiAcctKeysOnFollowsAttemptOrder(t *testing.T) {
	multiAcctThree(t)
	if err := SetKeyOn("relay", keyID(multiAcctKeyA), false); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", keyID(multiAcctKeyC)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, k := range multiAcctGet(t, "relay").KeysOn() {
		got = append(got, k.Key)
	}
	if !slices.Equal(got, []string{multiAcctKeyC, multiAcctKeyB}) {
		t.Fatalf("KeysOn: %v", got)
	}
}

func TestMultiAcctMaskedOutputNeverHasFullKey(t *testing.T) {
	multiAcctThree(t)
	_ = SetKeyOn("relay", keyID(multiAcctKeyA), false)
	_ = RemoveKey("relay", "deadbeef0000")
	_ = AddKey("relay", "dup", multiAcctKeyB)
	p := multiAcctGet(t, "relay")
	out := fmt.Sprint(p.KeyList())
	for _, k := range []string{multiAcctKeyA, multiAcctKeyB, multiAcctKeyC} {
		if strings.Contains(out, k) {
			t.Fatalf("KeyList leaks %q: %s", k, out)
		}
	}
}

func TestMultiAcctSignedInAccountTakesNoKeys(t *testing.T) {
	signIn(t)
	if err := AddKey("codex", "", "sk-account-0009"); err == nil {
		t.Fatal("account accepted an API key")
	} else if strings.Contains(err.Error(), "sk-account-0009") {
		t.Fatalf("error leaks the key: %v", err)
	}
}
