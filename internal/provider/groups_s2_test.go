package provider

import (
	"path/filepath"
	"slices"
	"testing"
)

// s2Home isolates the config and cache in a temp home with no signed-in
// accounts, so only the providers a test saves are in the catalog.
func s2Home(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func s2Save(t *testing.T, p Provider) {
	t.Helper()
	if err := Save(p); err != nil {
		t.Fatalf("Save(%s): %v", p.ID, err)
	}
}

// s2Two saves two providers that both offer model m: a as "m", b as "vendor/m".
func s2Two(t *testing.T) {
	t.Helper()
	s2Save(t, Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1", Models: []string{"m"}})
	s2Save(t, Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1", Models: []string{"vendor/m"}})
}

func s2IDs() []string {
	var out []string
	for _, e := range Catalog() {
		out = append(out, e.ID)
	}
	return out
}

func s2Refs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Ref())
	}
	return out
}

func TestS2GroupAutoDiscovery(t *testing.T) {
	s2Home(t)
	s2Two(t)

	g, ms, ok := FindGroup("auto-m")
	if !ok || !g.Auto || g.Name != "M" {
		t.Fatalf("auto group: %+v ok=%v", g, ok)
	}
	if !slices.Equal(s2Refs(ms), []string{"a/m", "b/vendor/m"}) {
		t.Fatalf("auto members in provider order: %v", s2Refs(ms))
	}
	if want := []string{"a/m", "b/vendor/m", "group/auto-m"}; !slices.Equal(s2IDs(), want) {
		t.Fatalf("catalog ids: %v, want %v", s2IDs(), want)
	}
	if e := Catalog()[2]; !e.Group || e.Name != "M" {
		t.Fatalf("group entry: %+v", e)
	}
	// nothing is stored for a discovered group
	if len(load().Groups) != 0 {
		t.Fatalf("auto group was persisted")
	}
}

func TestS2GroupNamesAndVariants(t *testing.T) {
	s2Home(t)
	s2Save(t, Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1", Models: []string{"claude-opus-5-5", "m:batch"}})
	s2Save(t, Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1", Models: []string{"anthropic/claude-opus-5.5", "m"}})

	gs := discoverGroups(All())
	var ids []string
	for _, g := range gs {
		ids = append(ids, g.ID)
	}
	// the colon variant is its own model and is not grouped with m
	if !slices.Equal(ids, []string{"auto-claude-opus-5-5"}) {
		t.Fatalf("discovered groups: %v", ids)
	}
	if gs[0].Name != "Claude Opus 5.5" {
		t.Fatalf("group name: %q", gs[0].Name)
	}
	if got := groupTitleKey("claude-opus-5-5"); got != "Claude Opus 5.5" {
		t.Fatalf("titleKey: %q", got)
	}
}

func TestS2GroupSaveRules(t *testing.T) {
	s2Home(t)
	s2Two(t)

	const empty = "组里至少要有一个模型"
	if err := SaveGroup(Group{ID: "mine", Members: []string{" ", ""}}); err == nil || err.Error() != empty {
		t.Fatalf("empty members: %v", err)
	}
	if err := SaveGroup(Group{ID: "mine", Members: []string{"group/auto-m"}}); err == nil || err.Error() != empty {
		t.Fatalf("only nested group: %v", err)
	}
	if err := SaveGroup(Group{ID: "Bad_ID", Members: []string{"a/m"}}); err == nil {
		t.Fatal("bad id accepted")
	}
	if err := Save(Provider{ID: "group", Name: "Group", Key: "k", Chat: "https://g.example/v1"}); err == nil {
		t.Fatal("provider id group accepted")
	}

	if err := SaveGroup(Group{ID: "mine", Members: []string{"b/vendor/m", "group/auto-m", "a/m", "a/m"}, Routing: "bogus", Affinity: "x"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("mine")
	if !ok || g.Auto || g.Routing != "" || g.Affinity != "" {
		t.Fatalf("saved group: %+v ok=%v", g, ok)
	}
	if !slices.Equal(s2Refs(ms), []string{"b/vendor/m", "a/m"}) {
		t.Fatalf("members, in order, nested group and repeat dropped: %v", s2Refs(ms))
	}
	// the user's groups come before the discovered ones
	if want := []string{"a/m", "b/vendor/m", "group/mine", "group/auto-m"}; !slices.Equal(s2IDs(), want) {
		t.Fatalf("catalog ids: %v, want %v", s2IDs(), want)
	}

	// name defaults to id, id to the slug of the name
	if err := SaveGroup(Group{Name: "Mine Two", Members: []string{"a/m"}, Routing: LeastUsed, Affinity: AffinityTurn}); err != nil {
		t.Fatal(err)
	}
	g2, _, ok := FindGroup("mine-two")
	if !ok || g2.Name != "Mine Two" || g2.Routing != LeastUsed || g2.Affinity != AffinityTurn {
		t.Fatalf("group by name: %+v ok=%v", g2, ok)
	}
	if err := SaveGroup(Group{ID: "solo", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g3, _, _ := FindGroup("solo"); g3.Name != "solo" {
		t.Fatalf("name defaults to id: %q", g3.Name)
	}
}

func TestS2GroupDeleteAndShow(t *testing.T) {
	s2Home(t)
	s2Two(t)

	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("hidden auto group still usable")
	}
	if slices.Contains(s2IDs(), "group/auto-m") {
		t.Fatal("hidden auto group still in the catalog")
	}
	if err := DeleteGroup("auto-m"); err == nil {
		t.Fatal("deleting a hidden group should fail")
	}
	if err := DeleteGroup("no-such"); err == nil {
		t.Fatal("deleting an unknown group should fail")
	}

	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("shown auto group: %v ok=%v", s2Refs(ms), ok)
	}

	// a user's own group is deleted outright
	if err := SaveGroup(Group{ID: "mine", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("mine"); ok {
		t.Fatal("deleted group still there")
	}
}

func TestS2GroupEditedAutoBecomesUsers(t *testing.T) {
	s2Home(t)
	s2Two(t)

	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-m")
	if !ok || g.Auto || !slices.Equal(s2Refs(ms), []string{"b/vendor/m"}) {
		t.Fatalf("edited group: %+v %v ok=%v", g, s2Refs(ms), ok)
	}
	if len(load().Groups) != 1 || load().Groups[0].Auto {
		t.Fatal("edited group should be stored as the user's")
	}
}

func TestS2GroupUnusableMembers(t *testing.T) {
	s2Home(t)
	s2Two(t)

	// a member whose model the provider does not offer, and one from a
	// provider that does not exist, leave the rest of the group usable
	if err := SaveGroup(Group{ID: "mixed", Members: []string{"zz/m", "a/nope", "a/m"}}); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("mixed"); !ok || !slices.Equal(s2Refs(ms), []string{"a/m"}) {
		t.Fatalf("mixed group: %v ok=%v", s2Refs(ms), ok)
	}
	if err := SaveGroup(Group{ID: "none", Members: []string{"zz/m"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("none"); ok {
		t.Fatal("a group with no usable member should not be ok")
	}
	if slices.Contains(s2IDs(), "group/none") {
		t.Fatal("a group with no usable member should not be in the catalog")
	}
}

func TestS2SetAffinity(t *testing.T) {
	s2Home(t)
	s2Two(t)

	if err := SetAffinity("a", AffinitySession); err != nil {
		t.Fatal(err)
	}
	p, err := Find("a")
	if err != nil || p.Affinity != AffinitySession || !slices.Equal(p.Models, []string{"m"}) {
		t.Fatalf("affinity set: %+v %v", p, err)
	}
	if err := SetAffinity("a", "bogus"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != AffinityAuto {
		t.Fatalf("unknown affinity should fall back to automatic, got %q", p.Affinity)
	}
	if err := SetAffinity("nope", AffinityOff); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
