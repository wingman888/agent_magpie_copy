package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// groupEnv is a clean config and cache, with the vendors' model lists saved
// as the vendors answered them.
func groupEnv(t *testing.T, lists map[string][]catalog.Model) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for id, ms := range lists {
		if err := catalog.SaveLive(id, "https://"+id+".example/v1", ms); err != nil {
			t.Fatal(err)
		}
		if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Chat: "https://" + id + ".example/v1"}); err != nil {
			t.Fatal(err)
		}
	}
}

func ids(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func TestDiscoveredGroupAndCatalogOrder(t *testing.T) {
	groupEnv(t, map[string][]catalog.Model{
		"a": {{ID: "m", Name: "m"}, {ID: "only-a", Name: "only-a"}},
		"b": {{ID: "vendor/m", Name: "Vendor M"}},
	})
	got := ids(Catalog())
	want := []string{"a/m", "a/only-a", "b/vendor/m", "group/auto-m"}
	if !slices.Equal(got, want) {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
	es := Catalog()
	g := es[len(es)-1]
	if g.Group == nil || g.Name != "Vendor M" || !g.Group.Auto {
		t.Fatalf("group entry = %+v", g)
	}
	// the group's name comes from the vendor that has a formal one, not from
	// the first vendor's bare id
	_, members, ok := FindGroup("group/auto-m")
	if !ok || len(members) != 2 || members[0].Name != "a/m" || members[1].Name != "b/vendor/m" {
		t.Fatalf("members = %+v, ok %v", members, ok)
	}
}

func TestSameModelUnderDifferentSpellingsGroups(t *testing.T) {
	groupEnv(t, map[string][]catalog.Model{
		"a": {{ID: "claude-opus-5-5", Name: "claude-opus-5-5"}, {ID: "m:batch", Name: "m:batch"}, {ID: "v1.beta", Name: "v1.beta"}},
		"b": {{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"}, {ID: "v1-beta", Name: "v1-beta"}},
		"c": {{ID: "Claude-Opus-5.5", Name: "Claude-Opus-5.5"}, {ID: "claude-opus-5-5-20260801", Name: "claude-opus-5-5-20260801"}},
	})
	var names []string
	for _, e := range Catalog() {
		if e.Group != nil {
			names = append(names, e.ID+"="+e.Name)
		}
	}
	// the batch variant and the dotted name stay models of their own, so
	// only the Claude Opus spellings form a group
	if !slices.Equal(names, []string{"group/auto-claude-opus-5-5=Claude Opus 5.5"}) {
		t.Fatalf("groups = %v", names)
	}
	_, members, _ := FindGroup("auto-claude-opus-5-5")
	if len(members) != 4 {
		t.Fatalf("members = %+v", members)
	}
}

func TestNormalizeModelKeyCases(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5":           "claude-opus-5-5",
		"anthropic/claude-opus-5.5": "claude-opus-5-5",
		"Claude-Opus-5.5":           "claude-opus-5-5",
		"claude-opus-5-5-20260801":  "claude-opus-5-5",
		"m:batch":                   "m:batch",
		"v1.beta":                   "v1.beta",
	} {
		if got := NormalizeModelKey(in); got != want {
			t.Errorf("NormalizeModelKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeleteAndShowAutoGroup(t *testing.T) {
	groupEnv(t, map[string][]catalog.Model{
		"a": {{ID: "m"}},
		"b": {{ID: "vendor/m"}},
	})
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Fatal("hidden group still found")
	}
	for _, e := range Catalog() {
		if e.Group != nil {
			t.Fatalf("hidden group listed: %s", e.ID)
		}
	}
	if err := DeleteGroup("auto-m"); err == nil {
		t.Fatal("deleting a hidden group should fail")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, members, ok := FindGroup("group/auto-m"); !ok || len(members) != 2 {
		t.Fatalf("shown group: ok %v members %+v", ok, members)
	}
}

func TestSaveGroupRules(t *testing.T) {
	groupEnv(t, map[string][]catalog.Model{
		"a": {{ID: "only-a"}},
		"b": {{ID: "vendor/m"}},
	})
	if err := SaveGroup(Group{Name: "Empty", Members: []string{" ", "group/x"}}); err == nil || err.Error() != "组里至少要有一个模型" {
		t.Fatalf("empty group: %v", err)
	}
	if err := Save(Provider{ID: "group", Name: "Group", Key: "k", Chat: "https://x.example/v1"}); err == nil {
		t.Fatal(`provider id "group" accepted`)
	}
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"b/vendor/m", "group/auto-m", "a/only-a", "b/vendor/m"}, Routing: "order", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	g, members, ok := FindGroup("group/mine")
	if !ok {
		t.Fatal("saved group not found")
	}
	if g.ID != "mine" || g.Name != "Mine" || g.Routing != Ordered || g.Affinity != "" {
		t.Fatalf("group = %+v", g)
	}
	if !slices.Equal(g.Members, []string{"b/vendor/m", "a/only-a"}) {
		t.Fatalf("members = %v (groups inside a group are ignored)", g.Members)
	}
	if len(members) != 2 || members[0].ProviderID != "b" || members[0].Model != "vendor/m" || members[1].ProviderID != "a" {
		t.Fatalf("resolved = %+v", members)
	}
	if err := DeleteGroup("nope"); err == nil {
		t.Fatal("deleting a missing group should fail")
	}
}

func TestSaveGroupReplacesDiscoveredGroup(t *testing.T) {
	groupEnv(t, map[string][]catalog.Model{
		"a": {{ID: "m"}},
		"b": {{ID: "vendor/m"}},
	})
	if err := SaveGroup(Group{ID: "auto-m", Name: "Only B", Members: []string{"b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	g, members, ok := FindGroup("auto-m")
	if !ok || g.Name != "Only B" || g.Auto || len(members) != 1 {
		t.Fatalf("replaced group = %+v %+v %v", g, members, ok)
	}
}

func TestSetAffinityAndRoutingValues(t *testing.T) {
	groupEnv(t, map[string][]catalog.Model{"a": {{ID: "m"}}})
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "session" {
		t.Fatalf("affinity = %q", p.Affinity)
	}
	if err := SetAffinity("a", "forever"); err == nil {
		t.Fatal("bad affinity accepted")
	}
	if err := SetRouting("a", "usage"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Routing != LeastUsed {
		t.Fatalf("routing = %q", p.Routing)
	}
	if err := SetRouting("a", "ordered"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Routing != "" {
		t.Fatalf("unknown routing kept as %q", p.Routing)
	}
}
