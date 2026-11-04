package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// keyHome isolates the config and the signed-in agents, as signIn does, but
// signs nobody in.
func keyHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func addCustom(t *testing.T, id, key string) {
	t.Helper()
	if err := Save(Provider{ID: id, Name: id, Key: key, Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

func keyMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// keyNames lists the names of a provider's keys in attempt order.
func keyNames(t *testing.T, id string) []string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range p.KeyList() {
		out = append(out, k.Name)
	}
	return out
}

func TestLegacySingleKeyGainsSecondKey(t *testing.T) {
	keyHome(t)
	addCustom(t, "relay", "sk-personal-0001")

	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, `"keys"`) || strings.Contains(s, `"keyName"`) {
		t.Fatalf("single-key config grew key fields: %s", s)
	}

	keyMust(t, AddKey("relay", "团队", "  sk-team-0002  "))
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	ks := p.KeyList()
	if len(ks) != 2 {
		t.Fatalf("KeyList: %+v", ks)
	}
	if !ks[0].Active || !ks[0].On || ks[0].Name != "" || ks[0].Masked != Mask("sk-personal-0001") {
		t.Fatalf("first key: %+v", ks[0])
	}
	if ks[1].Active || !ks[1].On || ks[1].Name != "团队" || ks[1].Masked != Mask("sk-team-0002") {
		t.Fatalf("second key: %+v", ks[1])
	}
	if p.Key != "sk-personal-0001" {
		t.Fatalf("the first key moved: %q", p.Key)
	}

	if err := AddKey("relay", "again", "sk-team-0002"); err == nil {
		t.Fatal("duplicate key accepted")
	} else if strings.Contains(err.Error(), "sk-team-0002") {
		t.Fatalf("error leaks the full key: %v", err)
	}
	if err := AddKey("relay", "blank", " \t "); err == nil {
		t.Fatal("empty key accepted")
	}
	if err := AddKey("relay", "dup-first", "sk-personal-0001"); err == nil {
		t.Fatal("duplicate of the first key accepted")
	}
	if got := keyNames(t, "relay"); !slices.Equal(got, []string{"", "团队"}) {
		t.Fatalf("failed adds changed the keys: %q", got)
	}
}

func TestUseKeyAndOrder(t *testing.T) {
	keyHome(t)
	addCustom(t, "relay", "sk-a-000001")
	keyMust(t, RenameKey("relay", refOf("sk-a-000001"), "个人"))
	keyMust(t, AddKey("relay", "团队", "sk-b-000002"))
	keyMust(t, AddKey("relay", "备份", "sk-c-000003"))

	if got := keyNames(t, "relay"); !slices.Equal(got, []string{"个人", "团队", "备份"}) {
		t.Fatalf("order after adds: %q", got)
	}

	// the front key is a no-op
	keyMust(t, UseKey("relay", refOf("sk-a-000001")))
	if got := keyNames(t, "relay"); !slices.Equal(got, []string{"个人", "团队", "备份"}) {
		t.Fatalf("UseKey on the front changed order: %q", got)
	}

	// a later key goes first; the old front stays enabled, right behind it
	keyMust(t, UseKey("relay", refOf("sk-c-000003")))
	if got := keyNames(t, "relay"); !slices.Equal(got, []string{"备份", "个人", "团队"}) {
		t.Fatalf("UseKey order: %q", got)
	}
	p, _ := Find("relay")
	for _, k := range p.KeyList() {
		if !k.On {
			t.Fatalf("UseKey switched a key off: %+v", k)
		}
	}
	if p.Key != "sk-c-000003" || p.KeyName != "备份" {
		t.Fatalf("first key fields: %q %q", p.Key, p.KeyName)
	}

	if err := UseKey("relay", "ffffff"); err == nil {
		t.Fatal("UseKey accepted an unknown key")
	}
}

func TestSwitchingKeysOffAndRemoving(t *testing.T) {
	keyHome(t)
	addCustom(t, "relay", "sk-a-000001")
	keyMust(t, RenameKey("relay", refOf("sk-a-000001"), "个人"))
	keyMust(t, AddKey("relay", "团队", "sk-b-000002"))
	keyMust(t, AddKey("relay", "备份", "sk-c-000003"))

	// switching off the front hands it to the next enabled key; the switched
	// off one stays in the list
	keyMust(t, SetKeyOn("relay", refOf("sk-a-000001"), false))
	p, _ := Find("relay")
	ks := p.KeyList()
	if len(ks) != 3 || ks[0].Name != "团队" || !ks[0].Active || !ks[0].On {
		t.Fatalf("after switching the front off: %+v", ks)
	}
	if ks[1].Name != "个人" || ks[1].On || ks[1].Active {
		t.Fatalf("switched-off key: %+v", ks[1])
	}

	// the switched-off key never gets used
	for _, c := range p.KeysOn() {
		if c.Key == "sk-a-000001" {
			t.Fatal("a switched-off key is listed by KeysOn")
		}
	}

	// with only one enabled key left, it can be neither switched off nor removed
	keyMust(t, SetKeyOn("relay", refOf("sk-c-000003"), false))
	before := keyNames(t, "relay")
	if err := SetKeyOn("relay", refOf("sk-b-000002"), false); err == nil {
		t.Fatal("last enabled key switched off")
	}
	if err := RemoveKey("relay", refOf("sk-b-000002")); err == nil {
		t.Fatal("last enabled key removed")
	}
	if got := keyNames(t, "relay"); !slices.Equal(got, before) {
		t.Fatalf("rejected change altered the keys: %q -> %q", before, got)
	}

	// enable another key, then the old front can be removed; the rest keep their names
	keyMust(t, SetKeyOn("relay", refOf("sk-c-000003"), true))
	keyMust(t, RemoveKey("relay", refOf("sk-b-000002")))
	p, _ = Find("relay")
	ks = p.KeyList()
	if len(ks) != 2 || ks[0].Name != "备份" || !ks[0].Active || ks[1].Name != "个人" || ks[1].On {
		t.Fatalf("after removing the front: %+v", ks)
	}
	if p.Key != "sk-c-000003" {
		t.Fatalf("the new front is not stored as key: %q", p.Key)
	}
}

func TestRenameKey(t *testing.T) {
	keyHome(t)
	addCustom(t, "relay", "sk-a-000001")
	keyMust(t, AddKey("relay", "团队", "sk-b-000002"))

	keyMust(t, RenameKey("relay", refOf("sk-b-000002"), "  新名字  "))
	if got := keyNames(t, "relay"); !slices.Equal(got, []string{"", "新名字"}) {
		t.Fatalf("rename: %q", got)
	}
	keyMust(t, RenameKey("relay", refOf("sk-b-000002"), "   "))
	keyMust(t, RenameKey("relay", refOf("sk-a-000001"), "个人"))
	if got := keyNames(t, "relay"); !slices.Equal(got, []string{"个人", ""}) {
		t.Fatalf("clearing a name: %q", got)
	}
}

func TestUnknownKeyRefIsAnError(t *testing.T) {
	keyHome(t)
	addCustom(t, "relay", "sk-a-000001")
	for name, err := range map[string]error{
		"UseKey":      UseKey("relay", "ffffff"),
		"SetKeyOn":    SetKeyOn("relay", "ffffff", false),
		"RemoveKey":   RemoveKey("relay", "ffffff"),
		"RenameKey":   RenameKey("relay", "ffffff", "x"),
		"empty ref":   RenameKey("relay", "", "x"),
		"no provider": AddKey("nosuch", "x", "sk-z"),
	} {
		if err == nil {
			t.Errorf("%s: no error for an unknown key or provider", name)
		}
	}
	if got := keyNames(t, "relay"); !slices.Equal(got, []string{""}) {
		t.Fatalf("failed operations changed the keys: %q", got)
	}
}

func TestSignedInAccountHasNoKeys(t *testing.T) {
	signIn(t)
	if err := AddKey("codex", "x", "sk-z-000009"); err == nil {
		t.Fatal("AddKey accepted a signed-in account")
	}
}

func TestKeysOnRestsFailedKey(t *testing.T) {
	keyHome(t)
	addCustom(t, "relay", "sk-a-000001")
	keyMust(t, AddKey("relay", "团队", "sk-b-000002"))

	ids := func() []string {
		p, _ := Find("relay")
		var out []string
		for _, c := range p.KeysOn() {
			out = append(out, c.Key)
		}
		return out
	}
	if got := ids(); !slices.Equal(got, []string{"sk-a-000001", "sk-b-000002"}) {
		t.Fatalf("KeysOn: %q", got)
	}

	MarkKeyFailed("relay", refOf("sk-a-000001"))
	if got := ids(); !slices.Equal(got, []string{"sk-b-000002", "sk-a-000001"}) {
		t.Fatalf("resting key still first: %q", got)
	}

	// resting is per key: the other provider is unaffected
	addCustom(t, "other", "sk-a-000001")
	p, _ := Find("other")
	if got := p.KeysOn(); len(got) != 1 || got[0].Key != "sk-a-000001" {
		t.Fatalf("other provider: %+v", got)
	}

	// a switched-off key is never listed, resting or not
	keyMust(t, SetKeyOn("relay", refOf("sk-b-000002"), false))
	if got := ids(); !slices.Equal(got, []string{"sk-a-000001"}) {
		t.Fatalf("switched-off key listed: %q", got)
	}
}
