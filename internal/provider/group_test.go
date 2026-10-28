package provider

import (
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func groupHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
}

func addProvider(t *testing.T, id string, models ...catalog.Model) {
	t.Helper()
	if err := catalog.SaveLive(id, "http://"+id, models); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: id, Chat: "http://" + id + "/v1", Key: "k-" + id, Icon: id + ".png"}); err != nil {
		t.Fatal(err)
	}
}

func ids(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func TestAutoGroupAndCatalogOrder(t *testing.T) {
	groupHome(t)
	addProvider(t, "a", catalog.Model{ID: "m", Name: "M"}, catalog.Model{ID: "only-a", Name: "Only"})
	addProvider(t, "b", catalog.Model{ID: "vendor/m", Name: "M"})
	want := []string{"a/m", "a/only-a", "b/vendor/m", "group/auto-m"}
	if got := ids(Catalog()); !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
	for _, e := range Catalog() {
		if e.ID == "group/auto-m" && (e.Group != "auto-m" || !reflect.DeepEqual(e.Icons, []string{"a.png", "b.png"})) {
			t.Errorf("group entry = %+v", e)
		}
	}
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a", "group/x"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	p, m, ok := Resolve("group/mine")
	if !ok || p.ID != "b" || m != "vendor/m" {
		t.Fatalf("resolve = %s %s %v", p.ID, m, ok)
	}
	g, ms, _ := FindGroup("group/mine")
	if len(g.Members) != 2 || len(ms) != 2 {
		t.Errorf("members = %v", g.Members)
	}
	gs := Groups()
	if gs[0].ID != "mine" || gs[1].ID != "auto-m" {
		t.Errorf("groups = %+v", gs)
	}
}

func TestDeleteShowGroup(t *testing.T) {
	groupHome(t)
	addProvider(t, "a", catalog.Model{ID: "m"})
	addProvider(t, "b", catalog.Model{ID: "m"})
	if _, _, ok := Resolve("group/auto-m"); !ok {
		t.Fatal("auto group missing")
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Resolve("group/auto-m"); ok {
		t.Error("hidden group resolves")
	}
	if gs := Groups(); len(gs) != 1 || !gs[0].Hidden {
		t.Errorf("groups = %+v", gs)
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Resolve("group/auto-m"); !ok {
		t.Error("shown group missing")
	}
	if err := DeleteGroup("nope"); err == nil {
		t.Error("deleting a missing group worked")
	}
	// taking over an auto group, then deleting it, hides the auto one
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if gs := Groups(); len(gs) != 1 || gs[0].Auto || len(gs[0].Members) != 1 {
		t.Errorf("groups = %+v", gs)
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := Resolve("group/auto-m"); ok {
		t.Error("still resolves")
	}
}

func TestSaveGroupValidation(t *testing.T) {
	groupHome(t)
	addProvider(t, "a", catalog.Model{ID: "m"})
	if err := SaveGroup(Group{ID: "Bad_ID", Members: []string{"a/m"}}); err == nil || !strings.Contains(err.Error(), "a group's id must be lowercase letters, digits and dashes") {
		t.Errorf("err = %v", err)
	}
	if err := SaveGroup(Group{ID: "x", Members: []string{" ", "group/y"}}); err == nil || err.Error() != "a group needs a model in it" {
		t.Errorf("err = %v", err)
	}
	if err := SaveGroup(Group{ID: " X ", Members: []string{"a/m", "a/m"}, Routing: "bogus", Affinity: "bogus", Auto: true}); err != nil {
		t.Fatal(err)
	}
	g := Groups()[0]
	if g.ID != "x" || g.Name != "x" || g.Routing != "" || g.Affinity != "" || g.Auto || len(g.Members) != 1 {
		t.Errorf("group = %+v", g)
	}
}

func TestNormalizeModel(t *testing.T) {
	same := []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801"}
	for _, s := range same {
		if got := normalizeModel(s); got != "claude-opus-5-5" {
			t.Errorf("%q → %q", s, got)
		}
	}
	if normalizeModel("anthropic/claude-opus-5.5:batch") == "claude-opus-5-5" {
		t.Error("variant merged")
	}
	if normalizeModel("gpt-5.1-codex") != normalizeModel("gpt-5-1-codex") {
		t.Error("gpt dots")
	}
	if normalizeModel("v1.beta") != "v1.beta" {
		t.Error("v1.beta changed")
	}
}

func TestAutoGroupNameAndEfforts(t *testing.T) {
	groupHome(t)
	addProvider(t, "openrouter", catalog.Model{ID: "anthropic/claude-opus-5.5", Name: "anthropic/claude-opus-5.5", Efforts: []string{"low", "high"}})
	addProvider(t, "copilot2", catalog.Model{ID: "claude-opus-5.5", Name: "claude-opus-5.5", Efforts: []string{"high", "max"}})
	addProvider(t, "claude2", catalog.Model{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"})
	var e Entry
	for _, x := range Catalog() {
		if x.Group != "" {
			e = x
		}
	}
	if e.ID != "group/auto-claude-opus-5-5" || e.Name != "Claude Opus 5.5" {
		t.Errorf("entry = %+v", e)
	}
	if len(e.Icons) != 3 {
		t.Errorf("icons = %v", e.Icons)
	}
}

func TestGroupEffortsIntersect(t *testing.T) {
	groupHome(t)
	addProvider(t, "a", catalog.Model{ID: "m", Efforts: []string{"low", "high"}})
	addProvider(t, "b", catalog.Model{ID: "m", Efforts: []string{"high", "max"}})
	for _, x := range Catalog() {
		if x.Group != "" && !reflect.DeepEqual(x.Efforts, []string{"high"}) {
			t.Errorf("efforts = %v", x.Efforts)
		}
	}
}

func TestProviderIDGroupAndAffinity(t *testing.T) {
	groupHome(t)
	err := Save(Provider{ID: "group", Chat: "http://x/v1", Key: "k"})
	if err == nil || err.Error() != `"group" starts the ids of routing groups; pick another id` {
		t.Errorf("err = %v", err)
	}
	addProvider(t, "a", catalog.Model{ID: "m"})
	if err := SetAffinity("a", AffinitySession); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != AffinitySession {
		t.Errorf("affinity = %q", p.Affinity)
	}
	if err := SetAffinity("a", "weird"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "" {
		t.Errorf("affinity = %q", p.Affinity)
	}
}
