package provider

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// f09GroupSetup gives a test its own config and cache, and no agent sign-ins.
func f09GroupSetup(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// f09TwoProviders has provider a offering m and only-a, and b offering vendor/m:
// the same model under two spellings, so magpie groups them as auto-m.
func f09TwoProviders(t *testing.T) {
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

// f09SeedLive records the model list a provider serves, as a fetch from the
// vendor would, so Available sees it.
func f09SeedLive(t *testing.T, provider string, ids ...string) {
	t.Helper()
	var ms []catalog.Model
	for _, id := range ids {
		ms = append(ms, catalog.Model{ID: id})
	}
	if err := catalog.SaveLive(provider, "", ms); err != nil {
		t.Fatal(err)
	}
}

// Discovery follows the models a provider serves, not only the ones it
// exposes: a exposes just only-a, yet serves m, so auto-m still gets a/m.
func TestF09AutoGroupFollowsServedModels(t *testing.T) {
	f09GroupSetup(t)
	for _, p := range []Provider{
		{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "ka", Models: []string{"only-a"}},
		{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "kb", Models: []string{"vendor/m"}},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	f09SeedLive(t, "a", "m", "only-a")
	f09SeedLive(t, "b", "vendor/m")
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto {
		t.Fatalf("auto-m not found: ok=%v auto=%v", ok, g.Auto)
	}
	if want := []string{"a/m", "b/vendor/m"}; !reflect.DeepEqual(f09MemberIDs(ms), want) {
		t.Errorf("auto-m members = %v, want %v", f09MemberIDs(ms), want)
	}
}

// With no list fetched from the vendor yet, the models the provider is
// configured to serve are what it groups by.
func TestF09AutoGroupWithoutLiveList(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto {
		t.Fatalf("auto-m not found without a live list: ok=%v auto=%v", ok, g.Auto)
	}
	if want := []string{"a/m", "b/vendor/m"}; !reflect.DeepEqual(f09MemberIDs(ms), want) {
		t.Errorf("auto-m members = %v, want %v", f09MemberIDs(ms), want)
	}
}

// A vendor list longer than manyModels is shown only in part by Exposed, but
// the group still sees every model the vendor serves.
func TestF09AutoGroupPastManyModels(t *testing.T) {
	f09GroupSetup(t)
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "ka"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "kb", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < manyModels+5; i++ {
		ids = append(ids, fmt.Sprintf("filler-%02d", i))
	}
	// m comes after the first manyModels, so only Available reaches it
	ids = append(ids, "m")
	f09SeedLive(t, "a", ids...)
	f09SeedLive(t, "b", "vendor/m")
	if got := len(f09ExposedOf(t, "a")); got != manyModels {
		t.Fatalf("a exposes %d models, want %d", got, manyModels)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto {
		t.Fatalf("auto-m not found past %d models: ok=%v auto=%v", manyModels, ok, g.Auto)
	}
	if want := []string{"a/m", "b/vendor/m"}; !reflect.DeepEqual(f09MemberIDs(ms), want) {
		t.Errorf("auto-m members = %v, want %v", f09MemberIDs(ms), want)
	}
}

// f09ExposedOf is the models the provider with the given id exposes to agents.
func f09ExposedOf(t *testing.T, id string) []catalog.Model {
	t.Helper()
	for _, p := range All() {
		if p.ID == id {
			return p.Exposed()
		}
	}
	t.Fatalf("no provider %q", id)
	return nil
}

// A user may take over an automatic group whose id carries a dot or a colon,
// and the other automatic groups are left alone.
func TestF09SaveGroupTakesOverDottedAutoGroup(t *testing.T) {
	f09GroupSetup(t)
	for _, p := range []Provider{
		{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "ka"},
		{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "kb"},
	} {
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	f09SeedLive(t, "a", "v1.beta", "claude-opus-5-5:batch")
	f09SeedLive(t, "b", "v1.beta", "claude-opus-5-5:batch")
	if _, _, ok := FindGroup("group/auto-v1.beta"); !ok {
		t.Fatal("auto-v1.beta not found as automatic")
	}
	if _, _, ok := FindGroup("group/auto-claude-opus-5-5:batch"); !ok {
		t.Fatal("auto-claude-opus-5-5:batch not found as automatic")
	}

	if err := SaveGroup(Group{ID: "auto-v1.beta", Members: []string{"b/v1.beta"}}); err != nil {
		t.Fatalf("take over auto-v1.beta: %v", err)
	}
	g, ms, ok := FindGroup("group/auto-v1.beta")
	if !ok || g.Auto || !reflect.DeepEqual(f09MemberIDs(ms), []string{"b/v1.beta"}) {
		t.Fatalf("taken over: ok=%v auto=%v members=%v", ok, g.Auto, f09MemberIDs(ms))
	}

	// the colon group is still automatic and still has both members
	g, ms, ok = FindGroup("group/auto-claude-opus-5-5:batch")
	if !ok || !g.Auto || len(ms) != 2 {
		t.Errorf("untouched colon group: ok=%v auto=%v members=%v", ok, g.Auto, f09MemberIDs(ms))
	}

	// an id that no automatic group has is still held to the plain form
	if err := SaveGroup(Group{ID: "Not.Auto", Members: []string{"a/v1.beta"}}); err == nil {
		t.Error(`id "Not.Auto": want an error`)
	}
	if err := SaveGroup(Group{ID: "auto-nothing.here", Members: []string{"a/v1.beta"}}); err == nil {
		t.Error(`id "auto-nothing.here": want an error`)
	}
}

func f09EntryIDs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func f09MemberIDs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Provider+"/"+m.Model)
	}
	return out
}

func TestF09GroupNeedsAMember(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
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

func TestF09ProviderIDGroupRejected(t *testing.T) {
	f09GroupSetup(t)
	if err := Save(Provider{ID: "group", Chat: "https://g.example/v1", Key: "k"}); err == nil {
		t.Error(`provider id "group": want an error`)
	}
}

func TestF09GroupInsideGroupIgnored(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
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
	if want := []string{"b/vendor/m", "a/m"}; !reflect.DeepEqual(f09MemberIDs(ms), want) {
		t.Errorf("resolved members = %v, want %v", f09MemberIDs(ms), want)
	}
}

func TestF09DeleteMissingGroupFails(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
	for _, id := range []string{"nope", "group/nope"} {
		if err := DeleteGroup(id); err == nil {
			t.Errorf("DeleteGroup(%q): want an error", id)
		}
	}
	if err := ShowGroup("nope"); err == nil {
		t.Error("ShowGroup of a missing group: want an error")
	}
}

func TestF09AutoGroupHiddenAndShown(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto {
		t.Fatalf("auto-m not found as automatic: ok=%v auto=%v", ok, g.Auto)
	}
	if want := []string{"a/m", "b/vendor/m"}; !reflect.DeepEqual(f09MemberIDs(ms), want) {
		t.Errorf("auto-m members = %v, want %v", f09MemberIDs(ms), want)
	}

	if err := DeleteGroup("group/auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Error("hidden auto-m still found")
	}
	for _, id := range f09EntryIDs(Catalog()) {
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

func TestF09CatalogListsProvidersThenGroups(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
	if err := SaveGroup(Group{ID: "mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"a/m", "a/only-a", "b/vendor/m", "group/mine", "group/auto-m"}
	if got := f09EntryIDs(Catalog()); !reflect.DeepEqual(got, want) {
		t.Errorf("catalog = %v, want %v", got, want)
	}
	for _, e := range Catalog() {
		if e.Group == "mine" && e.Name != "Mine" {
			t.Errorf("group named %q, want Mine", e.Name)
		}
	}
}

func TestF09SavedGroupKeepsMemberOrder(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
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
	if want := []string{"b/vendor/m", "a/only-a"}; !reflect.DeepEqual(f09MemberIDs(ms), want) {
		t.Errorf("members = %v, want %v", f09MemberIDs(ms), want)
	}
	if ms[0].Target.ID != "b" {
		t.Errorf("first member's provider = %q, want b", ms[0].Target.ID)
	}
}

func TestF09SaveGroupRules(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)

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
	if g, _, _ := FindGroup("old"); g.Routing != "" || g.Affinity != "session" {
		t.Errorf("usage/session: routing=%q affinity=%q, want \"\" and session", g.Routing, g.Affinity)
	}
}

func TestF09SaveGroupTakesOverAutoGroup(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-m")
	if !ok || g.Auto || len(ms) != 1 {
		t.Fatalf("taken over: ok=%v auto=%v members=%v", ok, g.Auto, f09MemberIDs(ms))
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
		t.Errorf("automatic group back: ok=%v auto=%v members=%v", ok, g.Auto, f09MemberIDs(ms))
	}
}

func TestF09DeleteUserGroup(t *testing.T) {
	f09GroupSetup(t)
	f09TwoProviders(t)
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

func TestF09NormRouting(t *testing.T) {
	cases := map[string]string{
		"order": Ordered, " Rotate ": Rotate, "least-used": LeastUsed,
		"usage": "", "bogus": "", "": "",
	}
	for in, want := range cases {
		if got := normRouting(in); got != want {
			t.Errorf("normRouting(%q) = %q, want %q", in, got, want)
		}
	}
}
