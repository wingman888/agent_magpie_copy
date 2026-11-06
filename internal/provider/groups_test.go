package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// groupSetup has two ready providers: A offers m and only-a, B offers its
// model as vendor/m, which is the same model (see normalizeModelKey).
func groupSetup(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := Save(Provider{ID: "a", Name: "A", Key: "k", Chat: "https://a.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Key: "k", Chat: "https://b.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("a", "https://a.example/v1", []catalog.Model{
		{ID: "m", Name: "M", Efforts: []string{"low", "high"}},
		{ID: "only-a", Name: "Only A", Efforts: []string{"low"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("b", "https://b.example/v1", []catalog.Model{
		{ID: "vendor/m", Name: "Vendor M", Efforts: []string{"high", "max"}},
	}); err != nil {
		t.Fatal(err)
	}
}

func catalogIDs() []string {
	var ids []string
	for _, e := range Catalog() {
		ids = append(ids, e.ID)
	}
	return ids
}

func memberRefs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.ProviderID+"/"+m.Model)
	}
	return out
}

// Providers A and B both serve m: the catalog lists each model, then the
// automatic group, named by the first display name; only A's only-a is not
// grouped.
func TestAutoGroupInCatalog(t *testing.T) {
	groupSetup(t)
	ids := catalogIDs()
	i, j, k := slices.Index(ids, "a/m"), slices.Index(ids, "b/vendor/m"), slices.Index(ids, "group/auto-m")
	if i < 0 || j <= i || k <= j {
		t.Fatalf("catalog order %v", ids)
	}
	if slices.Contains(ids, "group/auto-only-a") {
		t.Fatalf("a model only one provider serves was grouped: %v", ids)
	}
	var entry Entry
	for _, e := range Catalog() {
		if e.ID == "group/auto-m" {
			entry = e
		}
	}
	if entry.Name != "M" || !slices.Equal(entry.Efforts, []string{"high"}) || entry.Group != "auto-m" {
		t.Fatalf("auto entry %+v", entry)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto || g.Name != "M" {
		t.Fatalf("FindGroup auto-m: %+v %v", g, ok)
	}
	if got := memberRefs(ms); !slices.Equal(got, []string{"a/m", "b/vendor/m"}) {
		t.Fatalf("members %v", got)
	}
	// asked for its capabilities, a group answers as its first usable member
	rp, rm, ok := Resolve("group/auto-m")
	if !ok || rp.ID != "a" || rm != "m" {
		t.Fatalf("Resolve group/auto-m = %v %q %v, want first member a/m", rp.ID, rm, ok)
	}
}

// A saved group keeps its members in order, and shows in the catalog.
func TestSaveGroupKeepsOrder(t *testing.T) {
	groupSetup(t)
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("mine")
	if !ok || g.ID != "mine" || g.Name != "Mine" || g.Routing != "order" || g.Auto {
		t.Fatalf("group %+v %v", g, ok)
	}
	if got := memberRefs(ms); !slices.Equal(got, []string{"b/vendor/m", "a/only-a"}) {
		t.Fatalf("members %v", got)
	}
	if !slices.Contains(catalogIDs(), "group/mine") {
		t.Fatalf("catalog %v lacks group/mine", catalogIDs())
	}
}

// Deleting an automatic group hides it; showing it brings it back. An
// unknown group is an error.
func TestDeleteAndShowAutoGroup(t *testing.T) {
	groupSetup(t)
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatalf("hidden group still found")
	}
	if slices.Contains(catalogIDs(), "group/auto-m") {
		t.Fatalf("hidden group still listed")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("shown group: %v %v", ms, ok)
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Fatalf("deleting an unknown group should fail")
	}
}

// A user's group replaces an automatic one of the same id; deleting it
// hides the automatic one too.
func TestSaveOverAutoGroup(t *testing.T) {
	groupSetup(t)
	if err := SaveGroup(Group{ID: "auto-m", Name: "Mine M", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-m")
	if !ok || g.Auto || g.Name != "Mine M" || len(ms) != 1 {
		t.Fatalf("overridden: %+v %v", g, ok)
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatalf("deleted group still found")
	}
}

// Rules on saving: members required, the provider id group is taken, and a
// group inside a group is ignored, as are repeats.
func TestGroupRules(t *testing.T) {
	groupSetup(t)
	err := SaveGroup(Group{ID: "empty", Name: "Empty"})
	if err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Fatalf("empty members: %v", err)
	}
	if err := SaveGroup(Group{Name: "Only Group", Members: []string{"group/mine"}}); err == nil {
		t.Fatalf("a group with only a group as member should fail")
	}
	if err := Save(Provider{ID: "group", Name: "Group", Key: "k", Chat: "https://g.example/v1"}); err == nil {
		t.Fatalf("provider id group should fail")
	}
	if err := SaveGroup(Group{ID: "nest", Members: []string{"group/mine", "a/m"}}); err != nil {
		t.Fatal(err)
	}
	if _, ms, _ := FindGroup("nest"); !slices.Equal(memberRefs(ms), []string{"a/m"}) {
		t.Fatalf("nested group member not ignored: %v", memberRefs(ms))
	}
	if err := SaveGroup(Group{ID: "d", Members: []string{"a/m", "a/m", "b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	if _, ms, _ := FindGroup("d"); len(ms) != 2 {
		t.Fatalf("repeated member counted twice: %v", memberRefs(ms))
	}
	if err := SaveGroup(Group{Name: "My Team!", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("my-team"); !ok {
		t.Fatalf("id not made from name")
	}
	if err := SaveGroup(Group{ID: "r", Members: []string{"a/m"}, Routing: "usage", Affinity: "sometimes"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("r"); g.Routing != "" || g.Affinity != "" {
		t.Fatalf("invalid values kept: %+v", g)
	}
}

// A provider's affinity is set and kept; a bad one is refused.
func TestSetAffinity(t *testing.T) {
	groupSetup(t)
	if err := SetAffinity("a", "turn"); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("a"); err != nil || p.Affinity != "turn" {
		t.Fatalf("affinity %+v %v", p, err)
	}
	if err := SetAffinity("a", "sometimes"); err == nil {
		t.Fatalf("bad affinity should fail")
	}
}
