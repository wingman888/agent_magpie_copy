package provider

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

const multiAcctKeyD = "sk-extra-0004"

// editFor is what the editor posts: the provider's fields, with no key list.
func multiAcctEditFor(key string) Provider {
	return Provider{ID: "relay", Name: "Renamed", Key: key, Chat: "https://relay.example/v1"}
}

func TestMultiAcctSaveRenameKeepsAllKeys(t *testing.T) {
	multiAcctThree(t)
	if err := Save(multiAcctEditFor(multiAcctKeyA)); err != nil {
		t.Fatal(err)
	}
	p := multiAcctGet(t, "relay")
	if p.Name != "Renamed" {
		t.Fatalf("name not saved: %q", p.Name)
	}
	if !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyA, multiAcctKeyB, multiAcctKeyC)) {
		t.Fatalf("rename dropped keys: %v", multiAcctState(p))
	}
}

func TestMultiAcctSaveNewFrontKeyReplacesOnlyFront(t *testing.T) {
	multiAcctThree(t)
	if err := Save(multiAcctEditFor(multiAcctKeyD)); err != nil {
		t.Fatal(err)
	}
	p := multiAcctGet(t, "relay")
	if !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyD, multiAcctKeyB, multiAcctKeyC)) {
		t.Fatalf("new front key: %v", multiAcctState(p))
	}
	// the front key keeps its name; the others keep theirs
	if err := RenameKey("relay", keyID(multiAcctKeyB), "团队"); err != nil {
		t.Fatal(err)
	}
	if err := Save(multiAcctEditFor(multiAcctKeyD)); err != nil {
		t.Fatal(err)
	}
	if names := multiAcctNameList(multiAcctGet(t, "relay")); !slices.Equal(names, []string{"", "团队", ""}) {
		t.Fatalf("names after save: %v", names)
	}
}

func TestMultiAcctSaveWithoutKeyKeepsList(t *testing.T) {
	multiAcctThree(t)
	if err := Save(multiAcctEditFor("")); err != nil {
		t.Fatal(err)
	}
	if p := multiAcctGet(t, "relay"); !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyA, multiAcctKeyB, multiAcctKeyC)) {
		t.Fatalf("blank key dropped keys: %v", multiAcctState(p))
	}
}

func TestMultiAcctSaveLegacyShapeUnchanged(t *testing.T) {
	multiAcctLegacy(t, multiAcctKeyA)
	if err := Save(multiAcctEditFor(multiAcctKeyA)); err != nil {
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

func multiAcctNameList(p Provider) []string {
	var out []string
	for _, k := range p.KeyList() {
		out = append(out, k.Name)
	}
	return out
}

func TestMultiAcctEditKeysRefusesInvisibleProvider(t *testing.T) {
	multiAcctKeyHome(t)
	// a stored entry with no URL is only model picks; All and Find hide it
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "ghost", "name": "Ghost", "key": multiAcctKeyA,
	}}})
	before, _ := os.ReadFile(Path())
	if err := AddKey("ghost", "", multiAcctKeyB); err == nil {
		t.Fatal("AddKey wrote to an invisible provider")
	}
	if err := RemoveKey("nobody", keyID(multiAcctKeyA)); err == nil {
		t.Fatal("RemoveKey accepted an unknown provider")
	}
	after, _ := os.ReadFile(Path())
	if string(before) != string(after) {
		t.Fatalf("file changed for an invisible provider:\n%s", after)
	}
}

func TestMultiAcctHandWrittenKeysLoadInOrder(t *testing.T) {
	multiAcctKeyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "chat": "https://relay.example/v1",
		"key": multiAcctKeyA, "keyName": "个人",
		"keys": []any{
			map[string]any{"name": "团队", "key": multiAcctKeyB},
			map[string]any{"name": "免费", "key": multiAcctKeyC, "off": true},
			map[string]any{"key": multiAcctKeyD},
		},
	}}})
	p := multiAcctGet(t, "relay")
	wantState := []string{
		fmt.Sprintf("%s/true/true", keyID(multiAcctKeyA)),
		fmt.Sprintf("%s/false/true", keyID(multiAcctKeyB)),
		fmt.Sprintf("%s/false/false", keyID(multiAcctKeyC)),
		fmt.Sprintf("%s/false/true", keyID(multiAcctKeyD)),
	}
	if !slices.Equal(multiAcctState(p), wantState) {
		t.Fatalf("hand-written order/on: %v", multiAcctState(p))
	}
	if names := multiAcctNameList(p); !slices.Equal(names, []string{"个人", "团队", "免费", ""}) {
		t.Fatalf("names: %v", names)
	}
	on := p.KeysOn()
	if len(on) != 3 || on[0].Key != multiAcctKeyA || on[1].Key != multiAcctKeyB || on[2].Key != multiAcctKeyD {
		t.Fatalf("KeysOn: %+v", on)
	}
}

func TestMultiAcctFrontPromotedWhenKeyEmpty(t *testing.T) {
	multiAcctKeyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "chat": "https://relay.example/v1",
		"keys": []any{
			map[string]any{"name": "旧", "key": multiAcctKeyA, "off": true},
			map[string]any{"name": "团队", "key": multiAcctKeyB},
		},
	}}})
	p := multiAcctGet(t, "relay")
	if p.Key != multiAcctKeyB || p.KeyName != "团队" {
		t.Fatalf("front not promoted: key=%q name=%q", p.Key, p.KeyName)
	}
	if !slices.Equal(multiAcctState(p), []string{
		fmt.Sprintf("%s/true/true", keyID(multiAcctKeyB)),
		fmt.Sprintf("%s/false/false", keyID(multiAcctKeyA)),
	}) {
		t.Fatalf("state after promotion: %v", multiAcctState(p))
	}
}

func TestMultiAcctFrontPromotedWhenOnlyKeysGiven(t *testing.T) {
	multiAcctKeyHome(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "name": "Relay", "chat": "https://relay.example/v1",
		"keys": []any{map[string]any{"name": "只有", "key": multiAcctKeyC}},
	}}})
	p := multiAcctGet(t, "relay")
	if p.Key != multiAcctKeyC || p.KeyName != "只有" || len(p.Keys) != 0 {
		t.Fatalf("lone key not promoted: key=%q name=%q keys=%v", p.Key, p.KeyName, p.Keys)
	}
	if !slices.Equal(multiAcctState(p), multiAcctWant(multiAcctKeyC)) {
		t.Fatalf("state: %v", multiAcctState(p))
	}
}

func TestMultiAcctMaskIsRuneSafe(t *testing.T) {
	// 10 characters is under 16, so only two show at each end
	if got := Mask("甲乙丙丁戊己庚辛壬癸"); got != "甲乙…壬癸" || !utf8.ValidString(got) {
		t.Fatalf("Mask of multi-byte key: %q", got)
	}
	if got := Mask("甲乙丙丁戊己庚辛壬癸甲乙丙丁戊己庚辛"); got != "甲乙丙丁…戊己庚辛" || !utf8.ValidString(got) {
		t.Fatalf("Mask of long multi-byte key: %q", got)
	}
	if got := Mask("甲乙丙丁戊己甲乙"); got != "••••••••" {
		t.Fatalf("short multi-byte key: %q", got)
	}
}
