package provider_test

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func setup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func seed(t *testing.T) {
	setup(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.test/v1", Key: "sk-personal-1111"}); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptAddKey(t *testing.T) {
	seed(t)
	if err := provider.AddKey("relay", " 团队 ", "  sk-team-2222 "); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("relay", "x", "sk-team-2222"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := provider.AddKey("relay", "x", "sk-personal-1111"); err == nil {
		t.Fatal("duplicate of first accepted")
	}
	if err := provider.AddKey("relay", "x", "   "); err == nil {
		t.Fatal("empty accepted")
	}
	p, _ := provider.Find("relay")
	ks := p.KeyList()
	if len(ks) != 2 || !ks[0].Active || !ks[0].On || ks[1].Active || !ks[1].On || ks[1].Name != "团队" {
		t.Fatalf("%+v", ks)
	}
	if p.Key != "sk-personal-1111" || len(p.KeysOn()) != 2 || p.KeysOn()[1].Key != "sk-team-2222" || p.KeysOn()[1].Name != "团队" {
		t.Fatalf("%+v", p.KeysOn())
	}
	for _, k := range ks {
		if k.ID == "" || strings.Contains(k.Masked, "personal-1111") || strings.Contains(k.Masked, "team-2222") || k.Masked == "" {
			t.Fatalf("masked leak %+v", k)
		}
	}
}

func TestAcceptAddKeyToEmptyProvider(t *testing.T) {
	setup(t)
	if err := provider.Save(provider.Provider{ID: "bare", Name: "Bare", Chat: "https://bare.test/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.AddKey("bare", "only", "sk-only-9999"); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("bare")
	ks := p.KeyList()
	if p.Key != "sk-only-9999" || len(ks) != 1 || !ks[0].Active || ks[0].Name != "only" {
		t.Fatalf("%+v %+v", p, ks)
	}
}

func TestAcceptUseKeyAndRename(t *testing.T) {
	seed(t)
	provider.AddKey("relay", "Team", "sk-team-2222")
	p, _ := provider.Find("relay")
	ks := p.KeyList()
	if err := provider.UseKey("relay", ks[0].ID); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if p.Key != "sk-personal-1111" {
		t.Fatal("changed")
	}
	if err := provider.RenameKey("relay", ks[0].ID, " Personal "); err != nil {
		t.Fatal(err)
	}
	if err := provider.UseKey("relay", ks[1].ID); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	n := p.KeyList()
	if p.Key != "sk-team-2222" || len(n) != 2 || !n[0].Active || n[0].Name != "Team" || n[1].Name != "Personal" || !n[1].On || n[1].Active {
		t.Fatalf("%+v", n)
	}
	if on := p.KeysOn(); len(on) != 2 || on[1].Key != "sk-personal-1111" {
		t.Fatalf("%+v", on)
	}
	// clear name
	if err := provider.RenameKey("relay", ks[1].ID, "  "); err != nil {
		t.Fatal(err)
	}
	if err := provider.RenameKey("relay", ks[0].ID, ""); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	for _, k := range p.KeyList() {
		if k.Name != "" {
			t.Fatalf("%+v", k)
		}
	}
}

func TestAcceptOffAndRemove(t *testing.T) {
	seed(t)
	provider.AddKey("relay", "Team", "sk-team-2222")
	p, _ := provider.Find("relay")
	a, b := p.KeyList()[0].ID, p.KeyList()[1].ID
	if err := provider.SetKeyOn("relay", a, false); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	ks := p.KeyList()
	if p.Key != "sk-team-2222" || len(ks) != 2 || !ks[0].Active || ks[1].On || ks[1].ID != a || len(p.KeysOn()) != 1 {
		t.Fatalf("%+v", ks)
	}
	if err := provider.SetKeyOn("relay", b, false); err == nil {
		t.Fatal("turned off last")
	}
	if err := provider.RemoveKey("relay", b); err == nil {
		t.Fatal("removed last on")
	}
	p, _ = provider.Find("relay")
	if p.Key != "sk-team-2222" || len(p.KeyList()) != 2 {
		t.Fatal("changed after failed ops")
	}
	if err := provider.SetKeyOn("relay", a, true); err != nil {
		t.Fatal(err)
	}
	if err := provider.RemoveKey("relay", b); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	ks = p.KeyList()
	if p.Key != "sk-personal-1111" || len(ks) != 1 || !ks[0].Active || !ks[0].On {
		t.Fatalf("%+v", ks)
	}
}

func TestAcceptRemoveNonActiveAndOffKeys(t *testing.T) {
	seed(t)
	provider.AddKey("relay", "Team", "sk-team-2222")
	provider.AddKey("relay", "Free", "sk-free-3333")
	p, _ := provider.Find("relay")
	ks := p.KeyList()
	if err := provider.SetKeyOn("relay", ks[2].ID, false); err != nil {
		t.Fatal(err)
	}
	if err := provider.RemoveKey("relay", ks[1].ID); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	n := p.KeyList()
	if len(n) != 2 || n[0].Name != "" && n[0].Active == false || n[1].Name != "Free" || n[1].On {
		t.Fatalf("%+v", n)
	}
	// removing the off key is fine
	if err := provider.RemoveKey("relay", ks[2].ID); err != nil {
		t.Fatal(err)
	}
}

func TestAcceptUnknownRefAndAccount(t *testing.T) {
	seed(t)
	if provider.UseKey("relay", "nope") == nil || provider.SetKeyOn("relay", "nope", true) == nil ||
		provider.SetKeyOn("relay", "nope", false) == nil || provider.RemoveKey("relay", "nope") == nil ||
		provider.RenameKey("relay", "nope", "x") == nil {
		t.Fatal("unknown ref accepted")
	}
	if err := provider.AddKey("no-such-provider", "x", "sk-1"); err == nil {
		t.Fatal("unknown provider")
	}
}

func TestAcceptSingleKeyLegacyUnchanged(t *testing.T) {
	seed(t)
	p, _ := provider.Find("relay")
	ks := p.KeyList()
	if len(ks) != 1 || !ks[0].Active || !ks[0].On || ks[0].Name != "" || len(p.KeysOn()) != 1 {
		t.Fatalf("%+v", ks)
	}
	if err := provider.SetKeyOn("relay", ks[0].ID, false); err == nil {
		t.Fatal("off only key")
	}
	if err := provider.RemoveKey("relay", ks[0].ID); err == nil {
		t.Fatal("removed only key")
	}
}
