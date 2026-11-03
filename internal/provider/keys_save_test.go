package provider

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const keyD = "sk-extra-0004"

// editFor is what the editor posts: the provider's fields, with no key list.
func editFor(key string) Provider {
	return Provider{ID: "relay", Name: "Renamed", Key: key, Chat: "https://relay.example/v1"}
}

func TestSaveRenameKeepsAllKeys(t *testing.T) {
	three(t)
	if err := Save(editFor(keyA)); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	if p.Name != "Renamed" {
		t.Fatalf("name not saved: %q", p.Name)
	}
	if !slices.Equal(state(p), want(keyA, keyB, keyC)) {
		t.Fatalf("rename dropped keys: %v", state(p))
	}
}

func TestSaveNewFrontKeyReplacesOnlyFront(t *testing.T) {
	three(t)
	if err := Save(editFor(keyD)); err != nil {
		t.Fatal(err)
	}
	p := get(t, "relay")
	if !slices.Equal(state(p), want(keyD, keyB, keyC)) {
		t.Fatalf("new front key: %v", state(p))
	}
	// the front key keeps its name; the others keep theirs
	if err := RenameKey("relay", keyID(keyB), "团队"); err != nil {
		t.Fatal(err)
	}
	if err := Save(editFor(keyD)); err != nil {
		t.Fatal(err)
	}
	if names := nameList(get(t, "relay")); !slices.Equal(names, []string{"", "团队", ""}) {
		t.Fatalf("names after save: %v", names)
	}
}

func TestSaveWithoutKeyKeepsList(t *testing.T) {
	three(t)
	if err := Save(editFor("")); err != nil {
		t.Fatal(err)
	}
	if p := get(t, "relay"); !slices.Equal(state(p), want(keyA, keyB, keyC)) {
		t.Fatalf("blank key dropped keys: %v", state(p))
	}
}

func TestSaveLegacyShapeUnchanged(t *testing.T) {
	legacy(t, keyA)
	if err := Save(editFor(keyA)); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"keys"`, `"keyName"`, `"off"`} {
		if strings.Contains(string(b), field) {
			t.Fatalf("single-key config gained %s:\n%s", field, b)
		}
	}
}

func nameList(p Provider) []string {
	var out []string
	for _, k := range p.KeyList() {
		out = append(out, k.Name)
	}
	return out
}

func TestEditKeysRefusesInvisibleProvider(t *testing.T) {
	keyHome(t)
	// a stored entry with no URL is only model picks; All and Find hide it
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "ghost", "name": "Ghost", "key": keyA,
	}}})
	before, _ := os.ReadFile(Path())
	if err := AddKey("ghost", "", keyB); err == nil {
		t.Fatal("AddKey wrote to an invisible provider")
	}
	if err := RemoveKey("nobody", keyID(keyA)); err == nil {
		t.Fatal("RemoveKey accepted an unknown provider")
	}
	after, _ := os.ReadFile(Path())
	if string(before) != string(after) {
		t.Fatalf("file changed for an invisible provider:\n%s", after)
	}
}

func TestHandWrittenKeysLoadInOrder(t *testing.T) {
	keyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "chat": "https://relay.example/v1",
		"key": keyA, "keyName": "个人",
		"keys": []any{
			map[string]any{"name": "团队", "key": keyB},
			map[string]any{"name": "免费", "key": keyC, "off": true},
			map[string]any{"key": keyD},
		},
	}}})
	p := get(t, "relay")
	wantState := []string{
		fmt.Sprintf("%s/true/true", keyID(keyA)),
		fmt.Sprintf("%s/false/true", keyID(keyB)),
		fmt.Sprintf("%s/false/false", keyID(keyC)),
		fmt.Sprintf("%s/false/true", keyID(keyD)),
	}
	if !slices.Equal(state(p), wantState) {
		t.Fatalf("hand-written order/on: %v", state(p))
	}
	if names := nameList(p); !slices.Equal(names, []string{"个人", "团队", "免费", ""}) {
		t.Fatalf("names: %v", names)
	}
	on := p.KeysOn()
	if len(on) != 3 || on[0].Key != keyA || on[1].Key != keyB || on[2].Key != keyD {
		t.Fatalf("KeysOn: %+v", on)
	}
}

func TestFrontPromotedWhenKeyEmpty(t *testing.T) {
	keyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "chat": "https://relay.example/v1",
		"keys": []any{
			map[string]any{"name": "旧", "key": keyA, "off": true},
			map[string]any{"name": "团队", "key": keyB},
		},
	}}})
	p := get(t, "relay")
	if p.Key != keyB || p.KeyName != "团队" {
		t.Fatalf("front not promoted: key=%q name=%q", p.Key, p.KeyName)
	}
	if !slices.Equal(state(p), []string{
		fmt.Sprintf("%s/true/true", keyID(keyB)),
		fmt.Sprintf("%s/false/false", keyID(keyA)),
	}) {
		t.Fatalf("state after promotion: %v", state(p))
	}
}

func TestFrontPromotedWhenOnlyKeysGiven(t *testing.T) {
	keyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "chat": "https://relay.example/v1",
		"keys": []any{map[string]any{"name": "只有", "key": keyC}},
	}}})
	p := get(t, "relay")
	if p.Key != keyC || p.KeyName != "只有" || len(p.Keys) != 0 {
		t.Fatalf("lone key not promoted: key=%q name=%q keys=%v", p.Key, p.KeyName, p.Keys)
	}
	if !slices.Equal(state(p), want(keyC)) {
		t.Fatalf("state: %v", state(p))
	}
}

func TestMaskIsRuneSafe(t *testing.T) {
	if got := Mask("甲乙丙丁戊己庚辛壬癸"); got != "甲乙丙丁…庚辛壬癸" || !utf8.ValidString(got) {
		t.Fatalf("Mask of multi-byte key: %q", got)
	}
	if got := Mask("甲乙丙丁戊己甲乙"); got != "••••••••" {
		t.Fatalf("short multi-byte key: %q", got)
	}
}
