package provider

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// rgxGroupEnv isolates the config and the live model lists, as the other
// provider tests do.
func rgxGroupEnv(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// rgxLiveProvider saves a ready provider whose vendor list is models.
func rgxLiveProvider(t *testing.T, id string, models ...catalog.Model) {
	t.Helper()
	if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Chat: "https://" + id + ".test/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(id, "https://"+id+".test/v1", models); err != nil {
		t.Fatal(err)
	}
}

// rgxTwoSources: a and b both serve claude-opus-5.5 under different spellings
// (b with a display name), m and v1.beta; a alone serves m:batch and only-a.
func rgxTwoSources(t *testing.T) {
	t.Helper()
	rgxLiveProvider(t, "a",
		catalog.Model{ID: "claude-opus-5-5", Efforts: []string{"low", "high"}},
		catalog.Model{ID: "claude-opus-5-5-20260801"},
		catalog.Model{ID: "m"},
		catalog.Model{ID: "m:batch"},
		catalog.Model{ID: "v1.beta"},
		catalog.Model{ID: "only-a"},
	)
	rgxLiveProvider(t, "b",
		catalog.Model{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5", Efforts: []string{"low"}},
		catalog.Model{ID: "Vendor/V1.beta"},
		catalog.Model{ID: "m"},
	)
}

func rgxIds(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func TestRgxDiscoveryNormalizesOneModel(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)

	g, ms, ok := FindGroup("group/auto-claude-opus-5-5")
	if !ok {
		t.Fatal("no auto group for claude-opus-5-5")
	}
	if g.Name != "Claude Opus 5.5" || !g.Auto || g.Routing != "" || g.Affinity != "" {
		t.Fatalf("group %+v", g)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.ID)
	}
	// a's two spellings of one model count once; order follows the providers
	if !slices.Equal(got, []string{"a/claude-opus-5-5", "b/anthropic/claude-opus-5.5"}) {
		t.Fatalf("members %v", got)
	}
	// "m:batch" is not "m", and "v1.beta" is not "v1-beta"
	if _, _, ok := FindGroup("auto-m:batch"); ok {
		t.Fatal("m:batch merged into m")
	}
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("m not grouped across a and b")
	}
	if _, _, ok := FindGroup("auto-v1-beta"); ok {
		t.Fatal("v1.beta rewritten")
	}
	// b's "Vendor/V1.beta" is the same name once the vendor prefix goes
	if _, ms, ok := FindGroup("auto-v1.beta"); !ok || len(ms) != 2 {
		t.Fatalf("v1.beta members %v %v", ms, ok)
	}
	if _, _, ok := FindGroup("auto-only-a"); ok {
		t.Fatal("a model one provider serves formed a group")
	}
}

func TestRgxCatalogOrderProvidersThenGroups(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)
	es := Catalog()
	ia, ig := slices.Index(rgxIds(es), "a/only-a"), slices.Index(rgxIds(es), "group/auto-claude-opus-5-5")
	if ia < 0 || ig < 0 || ia > ig {
		t.Fatalf("catalog order %v", rgxIds(es))
	}
	for i, e := range es {
		if e.Kind == KindGroup && i < ia {
			t.Fatalf("group before providers: %v", rgxIds(es))
		}
	}
	var grp Entry
	for _, e := range es {
		if e.ID == "group/auto-claude-opus-5-5" {
			grp = e
		}
	}
	if grp.Name != "Claude Opus 5.5" || !slices.Equal(grp.Members, []string{"a/claude-opus-5-5", "b/anthropic/claude-opus-5.5"}) {
		t.Fatalf("entry %+v", grp)
	}
	// only the reasoning levels every member supports
	if !slices.Equal(grp.Efforts, []string{"low"}) {
		t.Fatalf("efforts %v", grp.Efforts)
	}
	if _, _, ok := Resolve("group/auto-claude-opus-5-5"); ok {
		t.Fatal("Resolve answered for a routing group")
	}
}

func TestRgxSaveGroupRules(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)

	if err := SaveGroup(Group{Name: "Mine"}); err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Fatalf("empty members: %v", err)
	}
	if err := SaveGroup(Group{ID: "Bad_Id", Members: []string{"a/only-a"}}); err == nil {
		t.Fatal("illegal id accepted")
	}
	if err := SaveGroup(Group{Name: "Two Words", Members: []string{"a/only-a"}, Routing: "ordered", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	f := load()
	if len(f.Groups) != 1 {
		t.Fatalf("groups %+v", f.Groups)
	}
	g := f.Groups[0]
	if g.ID != "two-words" || g.Name != "Two Words" || g.Routing != "" || g.Affinity != "" {
		t.Fatalf("saved %+v", g)
	}

	// a nested group is ignored; repeats count once; name defaults to id
	if err := SaveGroup(Group{ID: "mine", Members: []string{"group/two-words", "b/vendor/v1.beta", "a/only-a", "A/only-a", " "}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	g2, ms, ok := FindGroup("mine")
	if !ok || g2.Name != "mine" || g2.Routing != "order" || len(g2.Members) != 2 || len(ms) != 2 {
		t.Fatalf("mine %+v %v %v", g2, ms, ok)
	}
	if ms[0].ID != "b/vendor/v1.beta" || ms[1].ID != "a/only-a" {
		t.Fatalf("member order %v", ms)
	}
	if len(ms[1].Keys) != 1 || ms[1].Provider.ID != "a" {
		t.Fatalf("member %+v", ms[1])
	}

	// nothing but a nested group is still no members
	if err := SaveGroup(Group{ID: "nest", Members: []string{"group/mine"}}); err == nil {
		t.Fatal("nested-only group saved")
	}
}

func TestRgxDeleteShowAuto(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)

	if err := DeleteGroup("auto-claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-claude-opus-5-5"); ok {
		t.Fatal("hidden group still found")
	}
	if slices.Contains(rgxIds(Catalog()), "group/auto-claude-opus-5-5") {
		t.Fatal("hidden group still listed")
	}
	if err := DeleteGroup("auto-claude-opus-5-5"); err == nil {
		t.Fatal("deleting a hidden group succeeded")
	}
	if err := ShowGroup("auto-claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-claude-opus-5-5"); !ok || len(ms) != 2 {
		t.Fatalf("restored %v %v", ms, ok)
	}
	if err := ShowGroup("auto-claude-opus-5-5"); err != nil {
		t.Fatalf("showing a shown group: %v", err)
	}
	if err := ShowGroup("no-such"); err == nil {
		t.Fatal("showing an unknown group succeeded")
	}
	if err := DeleteGroup("no-such"); err == nil {
		t.Fatal("deleting an unknown group succeeded")
	}
}

func TestRgxEditingAutoGroupMakesItUsers(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)

	if err := SaveGroup(Group{ID: "auto-m", Name: "M", Members: []string{"b/m"}, Routing: "rotate"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-m")
	if !ok || g.Auto || g.Routing != "rotate" || len(ms) != 1 {
		t.Fatalf("edited %+v %v %v", g, ms, ok)
	}
	// deleting the user's copy of a discovered id hides it
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("deleted user copy still found")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if g, ms, ok := FindGroup("auto-m"); !ok || !g.Auto || len(ms) != 2 {
		t.Fatalf("back %+v %v %v", g, ms, ok)
	}
}

func TestRgxDeleteUserGroup(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)

	if err := SaveGroup(Group{ID: "mine", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("group/mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("mine"); ok {
		t.Fatal("deleted user group found")
	}
	if len(load().HiddenGroups) != 0 {
		t.Fatalf("a user group was hidden: %v", load().HiddenGroups)
	}
}

func TestRgxFindGroupNoReadyMember(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)
	if err := SaveGroup(Group{ID: "gone", Members: []string{"nobody/x", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("gone")
	if !ok || len(ms) != 1 || g.ID != "gone" {
		t.Fatalf("partial group %+v %v %v", g, ms, ok)
	}
	if err := SaveGroup(Group{ID: "none", Members: []string{"nobody/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("none"); ok || len(ms) != 0 {
		t.Fatalf("group with no usable member is ok: %v %v", ms, ok)
	}
	if slices.Contains(rgxIds(Catalog()), "group/none") {
		t.Fatal("group with no usable member listed")
	}
}

func TestRgxSetAffinity(t *testing.T) {
	rgxGroupEnv(t)
	rgxTwoSources(t)
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != AffinitySession {
		t.Fatalf("affinity %q", p.Affinity)
	}
	if err := SetAffinity("a", "bogus"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != AffinityAuto {
		t.Fatalf("bogus kept %q", p.Affinity)
	}
	if err := SetAffinity("nobody", "turn"); err == nil {
		t.Fatal("unknown provider accepted")
	}
}

func TestRgxGroupProviderIDRejected(t *testing.T) {
	rgxGroupEnv(t)
	if err := Save(Provider{ID: "group", Name: "G", Key: "k", Chat: "https://g.test/v1"}); err == nil {
		t.Fatal("provider id group accepted")
	}
}
