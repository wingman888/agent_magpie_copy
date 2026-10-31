package provider

import (
	"strings"
	"testing"
)

func keysSetup(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "sk-personal-1111", Chat: "https://r.example/v1"}); err != nil {
		t.Fatal(err)
	}
}

func ids(t *testing.T) (p *Provider, names []string, on []bool) {
	t.Helper()
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range p.KeyList() {
		names = append(names, k.Name)
		on = append(on, k.On)
	}
	return
}

func TestKeysLifecycle(t *testing.T) {
	keysSetup(t)
	if err := AddKey("relay", " 团队 ", " sk-team-22222222 "); err != nil {
		t.Fatal(err)
	}
	if AddKey("relay", "x", "sk-team-22222222") == nil || AddKey("relay", "x", "  ") == nil || AddKey("relay", "x", "sk-personal-1111") == nil {
		t.Fatal("dup/empty accepted")
	}
	p, _ := Find("relay")
	l := p.KeyList()
	if len(l) != 2 || !l[0].Active || !l[0].On || l[1].Active || !l[1].On || l[1].Name != "团队" {
		t.Fatalf("%+v", l)
	}
	for _, k := range l {
		if strings.Contains(k.Masked, "personal-1111") || strings.Contains(k.Masked, "team-22222222") {
			t.Fatal("leak")
		}
	}
	a, b := l[0].ID, l[1].ID
	if err := UseKey("relay", b); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-team-22222222" || p.KeyName != "团队" || len(p.Keys) != 1 || p.Keys[0].Off {
		t.Fatalf("%+v", p)
	}
	if err := UseKey("relay", b); err != nil {
		t.Fatal(err)
	}
	// disable front -> other takes over
	if err := SetKeyOn("relay", b, false); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	l = p.KeyList()
	if l[0].ID != a || !l[0].Active || l[1].ID != b || l[1].On {
		t.Fatalf("%+v", l)
	}
	if len(p.KeysOn()) != 1 {
		t.Fatal("keysOn")
	}
	if SetKeyOn("relay", a, false) == nil || RemoveKey("relay", a) == nil {
		t.Fatal("last enabled key removed")
	}
	if err := SetKeyOn("relay", b, true); err != nil {
		t.Fatal(err)
	}
	if err := RemoveKey("relay", a); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.Key != "sk-team-22222222" || p.KeyName != "团队" || len(p.Keys) != 0 {
		t.Fatalf("%+v", p)
	}
	if err := RenameKey("relay", b, " 主力 "); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("relay")
	if p.KeyName != "主力" {
		t.Fatal(p.KeyName)
	}
	RenameKey("relay", b, "")
	p, _ = Find("relay")
	if p.KeyName != "" {
		t.Fatal("not cleared")
	}
	for _, err := range []error{UseKey("relay", "nope"), SetKeyOn("relay", "nope", true), RemoveKey("relay", "nope"), RenameKey("relay", "nope", "x")} {
		if err == nil {
			t.Fatal("missing key accepted")
		}
	}
}

func TestAddKeyOnAccount(t *testing.T) {
	keysSetup(t)
	if err := AddKey("nonexistent", "", "k"); err == nil {
		t.Fatal("unknown provider")
	}
	if as := Accounts(); len(as) > 0 {
		if AddKey(as[0].ID, "", "k") == nil {
			t.Fatal("account accepted")
		}
	}
}
