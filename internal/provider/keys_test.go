package provider

import (
	"os"
	"strings"
	"testing"
)

func names(p *Provider) string {
	var s []string
	for _, k := range p.KeyList() {
		x := k.Name
		if !k.On {
			x += "!"
		}
		s = append(s, x)
	}
	return strings.Join(s, ",")
}

func setup(t *testing.T) {
	t.Helper()
	signIn(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-personal-1234567890", Chat: "https://r.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T) *Provider {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeysAddAndList(t *testing.T) {
	setup(t)
	if err := AddKey("relay", " 团队 ", " sk-team-abcdefghijkl "); err != nil {
		t.Fatal(err)
	}
	l := get(t).KeyList()
	if len(l) != 2 || !l[0].Active || !l[0].On || l[1].Active || !l[1].On || l[1].Name != "团队" {
		t.Fatalf("%+v", l)
	}
	if AddKey("relay", "x", "sk-team-abcdefghijkl") == nil || AddKey("relay", "x", "sk-personal-1234567890") == nil || AddKey("relay", "x", "  ") == nil {
		t.Fatal("duplicate/empty accepted")
	}
	for _, k := range l {
		if strings.Contains(k.Masked, "personal") || strings.Contains(k.Masked, "team-abc") || strings.Contains(k.ID, "sk-") {
			t.Fatalf("leak %+v", k)
		}
	}
	b, _ := os.ReadFile(Path())
	if !strings.Contains(string(b), `"keys"`) {
		t.Fatalf("not stored: %s", b)
	}
	// saving again without a key list keeps the keys
	p := get(t)
	p.Keys, p.KeyName = nil, ""
	if err := Save(*p); err != nil || len(get(t).KeyList()) != 2 {
		t.Fatalf("save dropped keys: %v", err)
	}
	if err := AddKey("codex", "a", "k"); err == nil {
		t.Fatal("account accepted a key")
	}
	if err := AddKey("nope", "a", "k"); err == nil {
		t.Fatal("unknown provider")
	}
}

func TestKeysUseOnRemove(t *testing.T) {
	setup(t)
	RenameKey("relay", get(t).KeyList()[0].ID, "个人")
	AddKey("relay", "团队", "sk-team-abcdefghijkl")
	AddKey("relay", "免费", "sk-free-abcdefghijkl")
	l := get(t).KeyList()
	a, b, c := l[0].ID, l[1].ID, l[2].ID
	if err := UseKey("relay", a); err != nil || names(get(t)) != "个人,团队,免费" {
		t.Fatal(err, names(get(t)))
	}
	if err := UseKey("relay", c); err != nil || names(get(t)) != "免费,个人,团队" {
		t.Fatal(err, names(get(t)))
	}
	UseKey("relay", a)
	if err := SetKeyOn("relay", c, false); err != nil || names(get(t)) != "个人,免费!,团队" {
		t.Fatal(err, names(get(t)))
	}
	if ks := get(t).KeysOn(); len(ks) != 2 {
		t.Fatal(ks)
	}
	if err := SetKeyOn("relay", a, false); err != nil || names(get(t)) != "团队,个人!,免费!" || !get(t).KeyList()[0].Active {
		t.Fatal(err, names(get(t)))
	}
	if get(t).Key != "sk-team-abcdefghijkl" {
		t.Fatal("front key not in Key")
	}
	before := names(get(t))
	if SetKeyOn("relay", b, false) == nil || RemoveKey("relay", b) == nil || names(get(t)) != before {
		t.Fatal("last enabled key was dropped")
	}
	SetKeyOn("relay", a, true)
	if err := RemoveKey("relay", b); err != nil || names(get(t)) != "个人,免费!" {
		t.Fatal(err, names(get(t)))
	}
	if err := RemoveKey("relay", c); err != nil || names(get(t)) != "个人" {
		t.Fatal(err, names(get(t)))
	}
	if get(t).Key != "sk-personal-1234567890" {
		t.Fatal("front")
	}
}

func TestKeysRenameAndMissing(t *testing.T) {
	setup(t)
	AddKey("relay", "团队", "sk-team-abcdefghijkl")
	l := get(t).KeyList()
	if err := RenameKey("relay", l[1].ID, "  新名 "); err != nil || get(t).KeyList()[1].Name != "新名" {
		t.Fatal(err)
	}
	if err := RenameKey("relay", l[0].ID, "主"); err != nil || get(t).KeyName != "主" {
		t.Fatal(err)
	}
	if err := RenameKey("relay", l[1].ID, ""); err != nil || get(t).KeyList()[1].Name != "" {
		t.Fatal(err)
	}
	if RenameKey("relay", "zzz", "x") == nil || UseKey("relay", "zzz") == nil || SetKeyOn("relay", "zzz", true) == nil || RemoveKey("relay", "zzz") == nil {
		t.Fatal("missing ref accepted")
	}
}
