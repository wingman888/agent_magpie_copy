package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// groupHome is a magpie with no signed-in agents and nothing configured.
func groupHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func addKeyed(t *testing.T, id string, models ...string) {
	t.Helper()
	if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Chat: "https://" + id + ".example/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
}

func entryIDs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func TestCanonicalModel(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5":           "claude-opus-5-5",
		"anthropic/claude-opus-5.5": "claude-opus-5-5",
		"Claude-Opus-5.5":           "claude-opus-5-5",
		"claude-opus-5-5-20260801":  "claude-opus-5-5",
		"claude-opus-5-5:batch":     "claude-opus-5-5:batch",
		"gemini-v1.beta":            "gemini-v1.beta",
		"openrouter/vendor/m":       "m",
		"gpt-4.1-mini":              "gpt-4-1-mini",
	} {
		if got := CanonicalModel(in); got != want {
			t.Errorf("CanonicalModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A model two providers serve under one name is a group of its own, listed
// after the providers' models.
func TestAutoGroupInCatalog(t *testing.T) {
	groupHome(t)
	addKeyed(t, "a", "m", "only-a")
	addKeyed(t, "b", "vendor/m")
	ids := entryIDs(Catalog())
	want := []string{"a/m", "a/only-a", "b/vendor/m", "group/auto-m"}
	if !slices.Equal(ids, want) {
		t.Fatalf("catalog %v, want %v", ids, want)
	}
	es := Catalog()
	last := es[len(es)-1]
	if !last.IsGroup() || last.Provider.ID != "group" || !last.Group.Auto || len(last.Members) != 2 {
		t.Fatalf("group entry %+v", last)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto || len(ms) != 2 || ms[0].ID != "a/m" || ms[1].ID != "b/vendor/m" || ms[1].Provider.ID != "b" || ms[1].Model != "vendor/m" {
		t.Fatalf("FindGroup: %+v %+v %v", g, ms, ok)
	}
	// asked what it can do, a group answers as its first member
	if p, m, ok := Resolve("group/auto-m"); !ok || p.ID != "a" || m != "m" {
		t.Fatalf("Resolve: %v %v %v", p.ID, m, ok)
	}
	// nothing about it was stored
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "auto-m") {
		t.Fatalf("a discovered group was stored: %s", b)
	}
}

func TestAutoGroupSpellingsAndName(t *testing.T) {
	groupHome(t)
	addKeyed(t, "p1", "claude-opus-5-5", "x:batch", "api-v1.beta")
	addKeyed(t, "p2")
	addKeyed(t, "p3", "Claude-Opus-5.5", "x", "api-v1-beta")
	addKeyed(t, "p4", "claude-opus-5-5-20260801")
	// p2 lists its models itself, with names
	if err := catalog.SaveLive("p2", "https://p2.example/v1", []catalog.Model{{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"}}); err != nil {
		t.Fatal(err)
	}
	gs := Groups()
	if len(gs) != 1 {
		t.Fatalf("groups %+v", gs)
	}
	g := gs[0]
	if g.ID != "auto-claude-opus-5-5" || g.Name != "Claude Opus 5.5" || !g.Auto ||
		!slices.Equal(g.Members, []string{"p1/claude-opus-5-5", "p2/anthropic/claude-opus-5.5", "p3/Claude-Opus-5.5", "p4/claude-opus-5-5-20260801"}) {
		t.Fatalf("group %+v", g)
	}
}

// The user's group routes as they made it; a group in a group, an empty
// group and a provider called "group" are refused or dropped.
func TestSaveGroup(t *testing.T) {
	groupHome(t)
	addKeyed(t, "a", "m", "only-a")
	addKeyed(t, "b", "vendor/m")
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a", "b/vendor/m", "group/auto-m"}, Routing: "order", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok || g.ID != "mine" || g.Name != "Mine" || g.Routing != Ordered || g.Affinity != "" || g.Auto ||
		!slices.Equal(g.Members, []string{"b/vendor/m", "a/only-a"}) || len(ms) != 2 || ms[0].Provider.ID != "b" {
		t.Fatalf("%+v %+v %v", g, ms, ok)
	}
	if _, _, ok := FindGroup("mine"); !ok {
		t.Fatal("bare id")
	}
	ids := entryIDs(Catalog())
	if !slices.Equal(ids, []string{"a/m", "a/only-a", "b/vendor/m", "group/mine", "group/auto-m"}) {
		t.Fatalf("catalog %v", ids)
	}
	if err := SaveGroup(Group{ID: "x", Routing: "ordered", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("group/x"); g.Name != "x" || g.Routing != "" {
		t.Fatalf("defaults: %+v", g)
	}
	if err := SaveGroup(Group{Name: "Empty"}); err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Fatalf("empty group: %v", err)
	}
	if err := SaveGroup(Group{Name: "Nested", Members: []string{"group/mine"}}); err == nil {
		t.Fatal("a group of groups was saved")
	}
	if err := Save(Provider{ID: "group", Key: "k", Chat: "https://g.example/v1"}); err == nil {
		t.Fatal(`a provider called "group" was saved`)
	}
	// a member nobody serves isn't reachable; a group without one isn't listed
	if err := SaveGroup(Group{ID: "gone", Members: []string{"nope/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, ms, ok := FindGroup("group/gone"); !ok || len(ms) != 0 || g.ID != "gone" {
		t.Fatalf("gone: %+v %v %v", g, ms, ok)
	}
	if slices.Contains(entryIDs(Catalog()), "group/gone") {
		t.Fatal("a group with nothing ready is listed")
	}
	if _, _, ok := Resolve("group/gone"); ok {
		t.Fatal("resolved a group with nothing ready")
	}
}

func TestDeleteAndShowGroup(t *testing.T) {
	groupHome(t)
	addKeyed(t, "a", "m")
	addKeyed(t, "b", "vendor/m")
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Fatal("hidden group found")
	}
	if hs := HiddenGroups(); len(hs) != 1 || hs[0].ID != "auto-m" {
		t.Fatalf("hidden %+v", hs)
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("group/auto-m"); !ok || len(ms) != 2 {
		t.Fatal("not shown again")
	}
	if err := DeleteGroup("nothing"); err == nil {
		t.Fatal("deleted a group that isn't there")
	}
	// changing a discovered group makes it the user's
	if err := DeleteGroup("group/auto-m"); err != nil {
		t.Fatal(err)
	}
	if err := SaveGroup(Group{ID: "auto-m", Name: "M", Members: []string{"b/vendor/m"}, Routing: "rotate"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || g.Auto || g.Name != "M" || len(ms) != 1 || g.Routing != Rotate {
		t.Fatalf("%+v %+v %v", g, ms, ok)
	}
	var f file
	b, _ := os.ReadFile(Path())
	json.Unmarshal(b, &f)
	if len(f.Groups) != 1 || len(f.HiddenGroups) != 0 {
		t.Fatalf("stored %s", b)
	}
	// a user's own group is deleted outright
	if err := SaveGroup(Group{ID: "mine", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("mine"); ok {
		t.Fatal("still there")
	}
}

func TestSetAffinityAndRouting(t *testing.T) {
	groupHome(t)
	addKeyed(t, "a", "m")
	for _, a := range []string{"session", "turn", "off", ""} {
		if err := SetAffinity("a", a); err != nil {
			t.Fatal(err)
		}
		if p, _ := Find("a"); p.Affinity != a {
			t.Fatalf("affinity %q, want %q", p.Affinity, a)
		}
	}
	if err := SetAffinity("a", "always"); err == nil {
		t.Fatal("bad affinity taken")
	}
	if err := SetAffinity("nobody", "off"); err == nil {
		t.Fatal("no such provider")
	}
	for in, want := range map[string]string{"order": Ordered, "rotate": Rotate, "least-used": LeastUsed, "usage": LeastUsed, "bogus": ""} {
		if err := SetRouting("a", in); err != nil {
			t.Fatal(err)
		}
		if p, _ := Find("a"); p.Routing != want {
			t.Fatalf("routing %q → %q, want %q", in, p.Routing, want)
		}
	}
	if LeastUsed != "least-used" || Ordered != "order" {
		t.Fatal("routing names")
	}
}

// Old files, without groups or affinity, still load.
func TestOldFileLoads(t *testing.T) {
	groupHome(t)
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	os.WriteFile(Path(), []byte(`{"providers":[{"id":"a","name":"A","key":"k","chat":"https://a.example/v1","models":["m"],"routing":"usage"}]}`), 0o600)
	p, err := Find("a")
	if err != nil || p.Routing != LeastUsed || p.Affinity != "" || len(Groups()) != 0 {
		t.Fatalf("%+v %v", p, err)
	}
}
