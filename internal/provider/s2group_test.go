package provider

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// s2Isolate points the config and cache at fresh directories.
func s2Isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestS2GroupsSaveFindDelete(t *testing.T) {
	s2Isolate(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(Save(Provider{ID: "a", Name: "A", Chat: "https://a.test/v1", Key: "sk-a-1111", Models: []string{"m"}}))
	must(Save(Provider{ID: "b", Name: "B", Chat: "https://b.test/v1", Key: "sk-b-2222", Models: []string{"vendor/m"}}))

	// providers first, then the discovered group
	var ids []string
	for _, e := range Catalog() {
		ids = append(ids, e.ID)
	}
	if want := []string{"a/m", "b/vendor/m", "group/auto-m"}; !slices.Equal(ids, want) {
		t.Fatalf("catalog %v, want %v", ids, want)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto || len(ms) != 2 || ms[0].Provider.ID != "a" || ms[1].Model != "vendor/m" {
		t.Fatalf("auto group %+v members %+v (%v)", g, ms, ok)
	}

	// deleting a discovered group hides it; showing it brings it back
	must(DeleteGroup("auto-m"))
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("hidden group still found")
	}
	if n := len(Catalog()); n != 2 {
		t.Fatalf("hidden group still listed: %d entries", n)
	}
	must(ShowGroup("group/auto-m"))
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("shown group not found")
	}

	// a user's group: group members are ignored, duplicates count once
	must(SaveGroup(Group{Name: "Mine Group", Members: []string{"b/vendor/m", "a/m", "group/auto-m", "a/m", " "}, Routing: "order", Affinity: "session"}))
	mine, ms, ok := FindGroup("mine-group")
	if !ok || mine.ID != "mine-group" || mine.Name != "Mine Group" || mine.Routing != Ordered || mine.Affinity != AffinitySession || mine.Auto {
		t.Fatalf("saved group %+v (%v)", mine, ok)
	}
	if len(ms) != 2 || ms[0].Provider.ID != "b" || ms[1].Provider.ID != "a" {
		t.Fatalf("resolved members %+v", ms)
	}
	for _, m := range mine.Members {
		if m == "group/auto-m" {
			t.Fatal("a group inside a group was kept")
		}
	}

	// saving over a discovered id makes that group the user's
	must(SaveGroup(Group{ID: "auto-m", Members: []string{"a/m"}}))
	over, ms, ok := FindGroup("auto-m")
	if !ok || over.Auto || len(ms) != 1 {
		t.Fatalf("overridden group %+v members %+v (%v)", over, ms, ok)
	}
	must(DeleteGroup("auto-m"))
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("deleted override still found")
	}
	must(ShowGroup("auto-m"))
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("discovered group not back after show")
	}

	// a group without members, a bad id, a provider called group, a missing group
	if err := SaveGroup(Group{ID: "empty"}); err == nil {
		t.Fatal("a group with no member was saved")
	}
	if err := SaveGroup(Group{ID: "nested", Members: []string{"group/mine-group"}}); err == nil {
		t.Fatal("a group with only groups as members was saved")
	}
	if err := SaveGroup(Group{ID: "Bad Id", Members: []string{"a/m"}}); err == nil {
		t.Fatal("a group id with a space was saved")
	}
	if err := Save(Provider{ID: "group", Name: "G", Chat: "https://g.test/v1", Key: "sk-g-3333"}); err == nil {
		t.Fatal(`a provider with id "group" was saved`)
	}
	if err := DeleteGroup("missing"); err == nil {
		t.Fatal("deleting a missing group did not fail")
	}
	if err := ShowGroup("missing"); err == nil {
		t.Fatal("showing a missing group did not fail")
	}

	// invalid routing and affinity fall back to the default
	must(SaveGroup(Group{ID: "r", Members: []string{"a/m"}, Routing: "usage", Affinity: "sometimes"}))
	r, _, _ := FindGroup("r")
	if r.Routing != LeastUsed || r.Affinity != "" {
		t.Fatalf("normalized group %+v", r)
	}
	must(SaveGroup(Group{ID: "r", Members: []string{"a/m"}, Routing: "ordered"}))
	if r, _, _ := FindGroup("r"); r.Routing != "" {
		t.Fatalf("invalid routing kept as %q", r.Routing)
	}
	must(DeleteGroup("mine-group"))
	if _, _, ok := FindGroup("mine-group"); ok {
		t.Fatal("deleted group still found")
	}

	// provider affinity
	must(SetAffinity("a", AffinityTurn))
	if p, _ := Find("a"); p.Affinity != AffinityTurn {
		t.Fatalf("affinity %q, want turn", p.Affinity)
	}
	must(SetAffinity("a", "nope"))
	if p, _ := Find("a"); p.Affinity != "" {
		t.Fatalf("invalid affinity kept as %q", p.Affinity)
	}
	if err := SetAffinity("missing", AffinityOff); err == nil {
		t.Fatal("affinity for a missing provider did not fail")
	}
}

func TestS2DiscoverGroupNames(t *testing.T) {
	offers := []offer{
		{Provider{ID: "a"}, catalog.Model{ID: "claude-opus-5-5", Name: "claude-opus-5-5"}},
		{Provider{ID: "b"}, catalog.Model{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"}},
		{Provider{ID: "c"}, catalog.Model{ID: "Claude-Opus-5.5"}},
		{Provider{ID: "c"}, catalog.Model{ID: "claude-opus-5-5-20260801"}},
		{Provider{ID: "a"}, catalog.Model{ID: "claude-opus-5-5:batch"}},
		{Provider{ID: "b"}, catalog.Model{ID: "v1.beta"}},
		{Provider{ID: "c"}, catalog.Model{ID: "v1.beta"}},
	}
	gs := discoverGroups(offers)
	if len(gs) != 2 {
		t.Fatalf("groups %+v, want 2", gs)
	}
	opus := gs[0]
	if opus.ID != "auto-claude-opus-5-5" || opus.Name != "Claude Opus 5.5" || !opus.Auto {
		t.Fatalf("opus group %+v", opus)
	}
	want := []string{"a/claude-opus-5-5", "b/anthropic/claude-opus-5.5", "c/Claude-Opus-5.5", "c/claude-opus-5-5-20260801"}
	if !slices.Equal(opus.Members, want) {
		t.Fatalf("opus members %v, want %v", opus.Members, want)
	}
	beta := gs[1]
	if beta.ID != "auto-v1-beta" || beta.Name != "v1.beta" || !slices.Equal(beta.Members, []string{"b/v1.beta", "c/v1.beta"}) {
		t.Fatalf("beta group %+v", beta)
	}
}
