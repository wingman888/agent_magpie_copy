package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Two models whose names slug alike (v1.beta and v1-beta) each keep an
// automatic group: the later one takes a numeric suffix, none is dropped.
func TestAutoGroupSlugCollisionKeepsBoth_f09(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	for _, id := range []string{"a", "b"} {
		up := "https://" + id + ".example/v1"
		if err := Save(Provider{ID: id, Name: id, Key: "k", Chat: up}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(id, up, []catalog.Model{{ID: "v1.beta"}, {ID: "v1-beta"}}); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, g := range allGroups(All()) {
		ids = append(ids, g.ID)
		if len(g.Members) != 2 {
			t.Fatalf("group %s members %v", g.ID, g.Members)
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"auto-v1-beta", "auto-v1-beta-2"}) {
		t.Fatalf("auto group ids %v", ids)
	}
}

// A group id asked for as a model resolves to its first usable member,
// the way a direct request for that member would.
func TestGroupResolvesToFirstMember_f09(t *testing.T) {
	groupSetup_f09(t)
	p, model, ok := Resolve("group/auto-m")
	if !ok || p.ID != "a" || model != "m" {
		t.Fatalf("Resolve(group/auto-m) = %s, %q, %v", p.ID, model, ok)
	}
	if _, _, ok := Resolve("group/no-such-group"); ok {
		t.Fatalf("an unknown group resolved")
	}
}
