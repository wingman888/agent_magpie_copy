package provider

import (
	"slices"
	"strings"
	"testing"
)

func keyProvider(t *testing.T) {
	t.Helper()
	signIn(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-personal-1111", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

func listOf(t *testing.T) []KeyInfo {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return p.KeyList()
}

func idOf(t *testing.T, key string) string {
	t.Helper()
	return keyID(key)
}

func names(l []KeyInfo) []string {
	var o []string
	for _, k := range l {
		o = append(o, k.Name)
	}
	return o
}

func TestAddKeyToLegacyConfig(t *testing.T) {
	keyProvider(t)
	if l := listOf(t); len(l) != 1 || !l[0].Active || !l[0].On {
		t.Fatalf("%+v", l)
	}
	if err := AddKey("relay", " 团队 ", "  sk-team-2222  "); err != nil {
		t.Fatal(err)
	}
	l := listOf(t)
	if len(l) != 2 || !l[0].Active || !l[0].On || l[1].Active || !l[1].On || l[1].Name != "团队" {
		t.Fatalf("%+v", l)
	}
	if strings.Contains(l[1].Masked, "team-2222") || l[1].Masked != Mask("sk-team-2222") {
		t.Fatalf("masked %q", l[1].Masked)
	}
	p, _ := Find("relay")
	if p.Key != "sk-personal-1111" || len(p.KeysOn()) != 2 {
		t.Fatalf("%+v", p)
	}
	for _, bad := range []string{"sk-team-2222", " sk-personal-1111", "  ", ""} {
		err := AddKey("relay", "x", bad)
		if err == nil || (bad != "" && strings.Contains(err.Error(), bad) && strings.TrimSpace(bad) != "") {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if AddKey("nope", "x", "k") == nil {
		t.Fatal("unknown provider")
	}
}

func TestAddKeyOnAccountFails(t *testing.T) {
	signIn(t)
	if AddKey("codex", "x", "sk-abc-12345678") == nil {
		t.Fatal("want error for signed-in account")
	}
}

func TestUseKey(t *testing.T) {
	keyProvider(t)
	AddKey("relay", "团队", "sk-team-2222")
	AddKey("relay", "免费", "sk-free-3333")
	team := idOf(t, "sk-team-2222")
	if err := UseKey("relay", listOf(t)[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := UseKey("relay", team); err != nil {
		t.Fatal(err)
	}
	l := listOf(t)
	if !slices.Equal(names(l), []string{"团队", "", "免费"}) || !l[0].Active || !l[1].On {
		t.Fatalf("%+v", l)
	}
	if p, _ := Find("relay"); p.Key != "sk-team-2222" || p.KeyName != "团队" {
		t.Fatalf("%+v", p)
	}
	if UseKey("relay", "zzzz") == nil {
		t.Fatal("unknown ref")
	}
	// using an off key turns it on
	free := idOf(t, "sk-free-3333")
	SetKeyOn("relay", free, false)
	if err := UseKey("relay", free); err != nil {
		t.Fatal(err)
	}
	if l := listOf(t); l[0].ID != free || !l[0].On {
		t.Fatalf("%+v", l)
	}
}

func TestSetKeyOnAndRemove(t *testing.T) {
	keyProvider(t)
	AddKey("relay", "团队", "sk-team-2222")
	first, team := idOf(t, "sk-personal-1111"), idOf(t, "sk-team-2222")
	if err := SetKeyOn("relay", first, false); err != nil {
		t.Fatal(err)
	}
	l := listOf(t)
	if l[0].ID != team || !l[0].Active || l[1].ID != first || l[1].On || len(l) != 2 {
		t.Fatalf("%+v", l)
	}
	before, _ := Find("relay")
	if SetKeyOn("relay", team, false) == nil || RemoveKey("relay", team) == nil {
		t.Fatal("last enabled key must stay")
	}
	after, _ := Find("relay")
	if after.Key != before.Key || len(after.Keys) != len(before.Keys) || after.Keys[0].Off != true {
		t.Fatalf("changed: %+v", after)
	}
	if err := SetKeyOn("relay", first, true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", team); err != nil {
		t.Fatal(err)
	}
	l = listOf(t)
	if len(l) != 1 || l[0].ID != first || !l[0].Active || !l[0].On {
		t.Fatalf("%+v", l)
	}
	// removing a disabled key is fine
	AddKey("relay", "x", "sk-x-99999999")
	x := idOf(t, "sk-x-99999999")
	SetKeyOn("relay", x, false)
	if err := RemoveKey("relay", x); err != nil || len(listOf(t)) != 1 {
		t.Fatalf("%v", err)
	}
	if SetKeyOn("relay", "nope", true) != nil == false || RemoveKey("relay", "nope") == nil {
		t.Fatal("unknown ref")
	}
}

func TestRenameKey(t *testing.T) {
	keyProvider(t)
	AddKey("relay", "团队", "sk-team-2222")
	if err := RenameKey("relay", idOf(t, "sk-personal-1111"), " 个人 "); err != nil {
		t.Fatal(err)
	}
	if err := RenameKey("relay", idOf(t, "sk-team-2222"), ""); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names(listOf(t)), []string{"个人", ""}) {
		t.Fatalf("%+v", listOf(t))
	}
	if RenameKey("relay", "nope", "x") == nil {
		t.Fatal("unknown ref")
	}
}
