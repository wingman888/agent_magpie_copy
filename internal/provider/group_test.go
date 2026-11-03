package provider

import (
	"path/filepath"
	"reflect"
	"testing"
)

// groupSetup gives a test its own config and cache, and no agent sign-ins.
func groupSetup(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// twoProviders has provider a offering m and only-a, and b offering vendor/m:
// the same model under two spellings, so magpie groups them as auto-m.
func twoProviders(t *testing.T) {
	t.Helper()
	for _, p := range []Provider{
		{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "ka", Models: []string{"m", "only-a"}},
		{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "kb", Models: []string{"vendor/m"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

func entryIDs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func memberIDs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Provider+"/"+m.Model)
	}
	return out
}

func TestGroupNeedsAMember(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	if err := SaveGroup(Group{ID: "x"}); err == nil || err.Error() != "组里至少要有一个模型" {
		t.Errorf("no members: err = %v", err)
	}
	// a group inside a group is ignored, which leaves nothing
	if err := SaveGroup(Group{ID: "x", Members: []string{"", "group/auto-m"}}); err == nil {
		t.Error("only a group as member: want an error")
	}
	if _, _, ok := FindGroup("group/x"); ok {
		t.Error("a rejected group was saved")
	}
}

func TestProviderIDGroupRejected(t *testing.T) {
	groupSetup(t)
	if err := Save(Provider{ID: "group", Chat: "https://g.example/v1", Key: "k"}); err == nil {
		t.Error(`provider id "group": want an error`)
	}
}

func TestGroupInsideGroupIgnored(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	err := SaveGroup(Group{ID: "mix", Members: []string{"group/auto-m", "b/vendor/m", "auto-m", "a/m", "b/vendor/m"}})
	if err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mix")
	if !ok {
		t.Fatal("group mix not found")
	}
	if want := []string{"b/vendor/m", "a/m"}; !reflect.DeepEqual(g.Members, want) {
		t.Errorf("stored members = %v, want %v", g.Members, want)
	}
	if want := []string{"b/vendor/m", "a/m"}; !reflect.DeepEqual(memberIDs(ms), want) {
		t.Errorf("resolved members = %v, want %v", memberIDs(ms), want)
	}
}

func TestDeleteMissingGroupFails(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	for _, id := range []string{"nope", "group/nope"} {
		if err := DeleteGroup(id); err == nil {
			t.Errorf("DeleteGroup(%q): want an error", id)
		}
	}
	if err := ShowGroup("nope"); err == nil {
		t.Error("ShowGroup of a missing group: want an error")
	}
}

func TestAutoGroupHiddenAndShown(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto {
		t.Fatalf("auto-m not found as automatic: ok=%v auto=%v", ok, g.Auto)
	}
	if want := []string{"a/m", "b/vendor/m"}; !reflect.DeepEqual(memberIDs(ms), want) {
		t.Errorf("auto-m members = %v, want %v", memberIDs(ms), want)
	}

	if err := DeleteGroup("group/auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Error("hidden auto-m still found")
	}
	for _, id := range entryIDs(Catalog()) {
		if id == "group/auto-m" {
			t.Error("hidden auto-m still in the catalog")
		}
	}

	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); !ok {
		t.Error("auto-m not back after ShowGroup")
	}
}

func TestCatalogListsProvidersThenGroups(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	if err := SaveGroup(Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a/m", "a/only-a", "b/vendor/m", "group/mine", "group/auto-m"}
	if got := entryIDs(Catalog()); !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
	for _, e := range Catalog() {
		if e.Group == "mine" && e.Name != "Mine" {
			t.Errorf("group named %q, want Mine", e.Name)
		}
	}
}

func TestSavedGroupKeepsMemberOrder(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	if err := SaveGroup(Group{ID: "mine", Members: []string{"b/vendor/m", "a/only-a", "b/vendor/m"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok {
		t.Fatal("group mine not found")
	}
	if g.ID != "mine" || g.Routing != "order" || g.Auto {
		t.Errorf("group = %+v", g)
	}
	if want := []string{"b/vendor/m", "a/only-a"}; !reflect.DeepEqual(memberIDs(ms), want) {
		t.Errorf("members = %v, want %v", memberIDs(ms), want)
	}
	if ms[0].Target.ID != "b" {
		t.Errorf("first member's provider = %q, want b", ms[0].Target.ID)
	}
}

func TestSaveGroupRules(t *testing.T) {
	groupSetup(t)
	twoProviders(t)

	if err := SaveGroup(Group{Name: "Mine Set", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("mine-set"); !ok || g.Name != "Mine Set" {
		t.Errorf("id from name: found=%v name=%q", ok, g.Name)
	}

	if err := SaveGroup(Group{ID: "Bad Id", Members: []string{"a/m"}}); err == nil {
		t.Error("invalid id: want an error")
	}
	if err := SaveGroup(Group{ID: "noslash", Members: []string{"m"}}); err == nil {
		t.Error("member without provider: want an error")
	}
	if err := SaveGroup(Group{Members: []string{"a/m"}}); err == nil {
		t.Error("no id and no name: want an error")
	}

	if err := SaveGroup(Group{ID: "odd", Members: []string{"a/m"}, Routing: "bogus", Affinity: "weird"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("odd"); g.Routing != "" || g.Affinity != "" {
		t.Errorf("invalid values kept: routing=%q affinity=%q", g.Routing, g.Affinity)
	}

	if err := SaveGroup(Group{ID: "old", Members: []string{"a/m"}, Routing: "usage", Affinity: "session"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("old"); g.Routing != LeastUsed || g.Affinity != "session" {
		t.Errorf("usage/session: routing=%q affinity=%q", g.Routing, g.Affinity)
	}
}

func TestSaveGroupTakesOverAutoGroup(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-m")
	if !ok || g.Auto || len(ms) != 1 {
		t.Fatalf("taken over: ok=%v auto=%v members=%v", ok, g.Auto, memberIDs(ms))
	}

	// removing the user's version hides the automatic group, which then
	// comes back as magpie builds it
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Error("removed group still found")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	g, ms, ok = FindGroup("auto-m")
	if !ok || !g.Auto || len(ms) != 2 {
		t.Errorf("automatic group back: ok=%v auto=%v members=%v", ok, g.Auto, memberIDs(ms))
	}
}

func TestDeleteUserGroup(t *testing.T) {
	groupSetup(t)
	twoProviders(t)
	if err := SaveGroup(Group{ID: "mine", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("mine"); ok {
		t.Error("deleted user group still found")
	}
}

func TestNormRouting(t *testing.T) {
	cases := map[string]string{
		"order": Ordered, " Rotate ": Rotate, "least-used": LeastUsed,
		"usage": LeastUsed, "bogus": "", "": "",
	}
	for in, want := range cases {
		if got := normRouting(in); got != want {
			t.Errorf("normRouting(%q) = %q, want %q", in, got, want)
		}
	}
}
