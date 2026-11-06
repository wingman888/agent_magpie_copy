package provider

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// groupsHome keeps a test's providers, groups and caches in a temporary home.
func groupsHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	return home
}

// Two vendors' names for one model compare equal; a colon variant and a dot
// that is not a version's stay as they are.
func TestCanonicalModel(t *testing.T) {
	same := []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801", "claude-opus-5.5-20260801"}
	for _, id := range same {
		if got := canonicalModel(id); got != "claude-opus-5-5" {
			t.Errorf("canonicalModel(%q) = %q", id, got)
		}
	}
	if got := canonicalModel("vendor/m:batch"); got != "m:batch" {
		t.Errorf("variant: %q", got)
	}
	if got := canonicalModel("m:batch"); got == canonicalModel("m") {
		t.Errorf("a variant is a model of its own")
	}
	if got := canonicalModel("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta: %q", got)
	}
}

// A model two providers offer under one name is an auto group, members in
// the order the providers were added; the group is what FindGroup and the
// catalog give.
func TestAutoGroupAcrossProviders(t *testing.T) {
	groupsHome(t)
	if err := Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !slices.Equal(g.Members, []string{"a/m", "b/vendor/m"}) || !g.Auto {
		t.Fatalf("auto group: %+v %v", g, ok)
	}
	if len(ms) != 2 || ms[0].Provider.ID != "a" || ms[0].Model != "m" || ms[0].Key.Key != "ka" || ms[1].Provider.ID != "b" || ms[1].Model != "vendor/m" {
		t.Fatalf("members: %+v", ms)
	}
	var ids []string
	for _, e := range Catalog() {
		ids = append(ids, e.ID)
	}
	want := []string{"a/m", "b/vendor/m", "group/auto-m"}
	if !slices.Equal(ids, want) {
		t.Fatalf("catalog %v, want %v", ids, want)
	}
	if got, _, _ := FindGroup("auto-m"); got.ID != "auto-m" {
		t.Fatal("the id without its prefix finds the group too")
	}
}

// The group takes the display name a provider gives its model, not the id.
func TestAutoGroupNamedForDisplay(t *testing.T) {
	groupsHome(t)
	if err := catalog.SaveLive("a", "https://a.example/v1", []catalog.Model{{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1", Models: []string{"anthropic/claude-opus-5.5"}}); err != nil {
		t.Fatal(err)
	}
	g, _, ok := FindGroup("group/auto-claude-opus-5-5")
	if !ok || g.Name != "Claude Opus 5.5" {
		t.Fatalf("%+v %v", g, ok)
	}
}

// Deleting an auto group hides it until ShowGroup brings it back.
func TestDeleteAndShowAutoGroup(t *testing.T) {
	groupsHome(t)
	Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1", Models: []string{"m"}})
	Save(Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1", Models: []string{"vendor/m"}})
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Fatal("hidden group still found")
	}
	for _, e := range Catalog() {
		if e.ID == "group/auto-m" {
			t.Fatal("hidden group still in the catalog")
		}
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("group/auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("shown group: %v %+v", ok, ms)
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Fatal("deleting a group that isn't there should fail")
	}
}

// A user's group keeps the order of its members, drops groups inside it,
// and turns invalid routing and affinity values into the defaults.
func TestSaveGroup(t *testing.T) {
	groupsHome(t)
	Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1", Models: []string{"only-a"}})
	Save(Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1", Models: []string{"vendor/m"}})

	if err := SaveGroup(Group{Name: "Empty", Members: []string{"group/x"}}); err == nil || err.Error() != "组里至少要有一个模型" {
		t.Fatalf("empty group: %v", err)
	}
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a", "group/auto-m"}, Routing: "order", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok || g.ID != "mine" || g.Routing != Ordered || g.Affinity != "" || !slices.Equal(g.Members, []string{"b/vendor/m", "a/only-a"}) {
		t.Fatalf("saved: %+v %v", g, ok)
	}
	if len(ms) != 2 || ms[0].Provider.ID != "b" || ms[1].Provider.ID != "a" {
		t.Fatalf("member order: %+v", ms)
	}
	if err := SaveGroup(Group{Name: "Other", Members: []string{"a/only-a"}, Routing: "ordered"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("other"); g.Routing != "" {
		t.Fatalf("ordered is not a routing: %q", g.Routing)
	}

	// saving an auto group makes it the user's
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "rotate"}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("auto-m"); !ok || g.Auto || g.Routing != Rotate {
		t.Fatalf("user's auto group: %+v %v", g, ok)
	}

	if err := Save(Provider{ID: "group", Name: "G", Key: "k", Chat: "https://g.example/v1"}); err == nil {
		t.Fatal(`a provider can't be called "group"`)
	}
}

// Affinity is kept for a provider as one of its three values, or automatic.
func TestSetAffinity(t *testing.T) {
	groupsHome(t)
	Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1"})
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != AffinitySession {
		t.Fatalf("affinity %q", p.Affinity)
	}
	SetAffinity("a", "bogus")
	if p, _ := Find("a"); p.Affinity != "" {
		t.Fatalf("bogus affinity kept: %q", p.Affinity)
	}
}

// Keys split by how well they suit the request's protocol: the best tier is
// the pool, the rest are asides, unspecified before another protocol's.
func TestPoolKeys(t *testing.T) {
	keys := []KeyAccount{{Key: "k1"}, {Key: "k2", Protocol: Chat}, {Key: "k3", Protocol: Anthropic}}
	main, aside := PoolKeys(keys, Chat)
	if len(main) != 1 || main[0].Key != "k2" || len(aside) != 2 || aside[0].Key != "k1" || aside[1].Key != "k3" {
		t.Fatalf("chat: %+v / %+v", main, aside)
	}
	main, aside = PoolKeys([]KeyAccount{{Key: "k1"}, {Key: "k2"}, {Key: "k3", Protocol: Chat}}, Chat)
	if len(main) != 1 || main[0].Key != "k3" || len(aside) != 2 || aside[0].Key != "k1" || aside[1].Key != "k2" {
		t.Fatalf("two unspecified, one chat: %+v / %+v", main, aside)
	}
	main, aside = PoolKeys([]KeyAccount{{Key: "k1"}, {Key: "k2"}}, Anthropic)
	if len(main) != 2 || len(aside) != 0 {
		t.Fatalf("no key is made for a protocol: %+v / %+v", main, aside)
	}
}

// The first key takes the provider's KeyProtocol.
func TestKeysForUsesKeyProtocol(t *testing.T) {
	p := Provider{Key: "k1", KeyProtocol: Anthropic, Keys: []KeyAccount{{Key: "k2"}, {Key: "k3", Protocol: Chat}}}
	main, aside := p.KeysFor(Anthropic)
	if len(main) != 1 || main[0].Key != "k1" || len(aside) != 2 || aside[0].Key != "k2" || aside[1].Key != "k3" {
		t.Fatalf("anthropic: %+v / %+v", main, aside)
	}
}
