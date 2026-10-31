package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func groupSetup(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func addModels(t *testing.T, id string, models ...string) {
	t.Helper()
	if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Chat: "https://" + id + ".test/v1", Key: "k-" + id, Models: models}); err != nil {
		t.Fatal(err)
	}
}

func TestNormalModel(t *testing.T) {
	same := []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801", "openrouter/anthropic/claude-opus-5.5-20260801"}
	for _, id := range same {
		if got := NormalModel(id); got != "claude-opus-5-5" {
			t.Errorf("%s → %s", id, got)
		}
	}
	for in, want := range map[string]string{
		"claude-opus-5-5:batch": "claude-opus-5-5:batch",
		"Vendor/m:Batch":        "m:batch",
		"gemini-v1.beta":        "gemini-v1.beta",
		"gpt-4.1-mini":          "gpt-4-1-mini",
		"x-1.2.3":               "x-1-2-3",
		"m":                     "m",
	} {
		if got := NormalModel(in); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
	if NormalModel("a:batch") == NormalModel("a") {
		t.Error("a variant is another model")
	}
}

func TestAutoGroupAndCatalog(t *testing.T) {
	groupSetup(t)
	addModels(t, "a", "m", "only-a")
	addModels(t, "b", "vendor/m")
	es := Catalog()
	var ids []string
	for _, e := range es {
		ids = append(ids, e.ID)
	}
	if want := []string{"a/m", "a/only-a", "b/vendor/m", "group/auto-m"}; !slices.Equal(ids, want) {
		t.Fatalf("catalog %v, want %v", ids, want)
	}
	g := es[len(es)-1]
	if g.Group == nil || !g.Group.Auto || !slices.Equal(g.Group.Members, []string{"a/m", "b/vendor/m"}) {
		t.Fatalf("group %+v", g.Group)
	}
	if _, ms, ok := FindGroup("group/auto-m"); !ok || len(ms) != 2 || ms[0].Provider.ID != "a" || ms[1].Model != "vendor/m" {
		t.Fatalf("find %v %v", ms, ok)
	}
	if p, model, ok := Resolve("group/auto-m"); !ok || p.ID != "a" || model != "m" {
		t.Fatalf("a group answers as its first member: %v %v %v", p.ID, model, ok)
	}

	// removing it hides it; showing brings it back
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("hidden group found")
	}
	if err := DeleteGroup("auto-m"); err == nil {
		t.Fatal("deleted a group that is not there")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("group not back")
	}

	// editing makes it the user's, and it stays when the models change
	if err := SaveGroup(Group{ID: "auto-m", Name: "Mine", Members: []string{"b/vendor/m"}, Routing: "ordered", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	g2, ms, _ := FindGroup("auto-m")
	if g2.Auto || g2.Name != "Mine" || len(ms) != 1 || g2.Routing != "" || g2.Affinity != "" {
		t.Fatalf("edited %+v %v", g2, ms)
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("deleted group still there")
	}
}

func TestAutoGroupNameAndSpellings(t *testing.T) {
	groupSetup(t)
	addModels(t, "a", "claude-opus-5-5")
	addModels(t, "b", "anthropic/claude-opus-5.5")
	addModels(t, "c", "claude-opus-5-5-20260801", "claude-opus-5-5:batch")
	// b lists a formal name, the others only their ids
	if err := catalog.SaveLive("b", "https://b.test/v1", []catalog.Model{{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"}}); err != nil {
		t.Fatal(err)
	}
	gs := Groups()
	if len(gs) != 1 || gs[0].ID != "auto-claude-opus-5-5" || gs[0].Name != "Claude Opus 5.5" || len(gs[0].Members) != 3 {
		t.Fatalf("groups %+v", gs)
	}
	addModels(t, "d", "something-else")
	if len(Groups()) != 1 {
		t.Fatal("different names grouped")
	}
}

func TestSaveGroupRules(t *testing.T) {
	groupSetup(t)
	addModels(t, "a", "m")
	if err := SaveGroup(Group{ID: "x"}); err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Fatalf("empty members: %v", err)
	}
	if err := SaveGroup(Group{ID: "x", Members: []string{"group/y"}}); err == nil {
		t.Fatal("a group in a group")
	}
	if err := SaveGroup(Group{Name: "My Team", Members: []string{"a/m", "group/y", "a/m"}, Routing: "rotate", Affinity: "turn"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/my-team")
	if !ok || g.Name != "My Team" || g.Routing != "rotate" || g.Affinity != "turn" || !slices.Equal(g.Members, []string{"a/m"}) || len(ms) != 1 {
		t.Fatalf("%+v %v %v", g, ms, ok)
	}
	if err := SaveGroup(Group{ID: "only-id", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("only-id"); g.Name != "only-id" {
		t.Fatalf("name defaults to id: %+v", g)
	}
	if err := SaveGroup(Group{ID: "Bad Id!", Members: []string{"a/m"}}); err == nil {
		t.Fatal("bad id")
	}
	if err := DeleteGroup("nope"); err == nil {
		t.Fatal("deleted nothing")
	}
	if err := Save(Provider{ID: "group", Name: "G", Chat: "https://g.test/v1", Key: "k"}); err == nil {
		t.Fatal(`provider id "group"`)
	}
	// a member whose provider is gone leaves the group with no model ready
	if err := SaveGroup(Group{ID: "ghost", Members: []string{"nowhere/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("ghost"); !ok || len(ms) != 0 {
		t.Fatalf("%v %v", ms, ok)
	}
	for _, e := range Catalog() {
		if e.ID == "group/ghost" {
			t.Fatal("a group with no model ready is listed")
		}
	}
}

func TestGroupEffortsAreTheCommonOnes(t *testing.T) {
	groupSetup(t)
	addModels(t, "a", "m")
	addModels(t, "b", "m")
	catalog.SaveLive("a", "https://a.test/v1", []catalog.Model{{ID: "m", Efforts: []string{"low", "medium", "high"}}})
	catalog.SaveLive("b", "https://b.test/v1", []catalog.Model{{ID: "m", Efforts: []string{"medium", "high", "xhigh"}}})
	es := Catalog()
	if g := es[len(es)-1]; g.ID != "group/auto-m" || !slices.Equal(g.Efforts, []string{"medium", "high"}) {
		t.Fatalf("%+v", g)
	}
}

func TestAffinitySetting(t *testing.T) {
	groupSetup(t)
	addModels(t, "a", "m")
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "session" {
		t.Fatal(p.Affinity)
	}
	SetAffinity("a", "forever")
	if p, _ := Find("a"); p.Affinity != "" {
		t.Fatal(p.Affinity)
	}
	if err := SetRouting("a", "usage"); err != nil { // the old spelling
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Routing != LeastUsed || LeastUsed != "least-used" {
		t.Fatal(p.Routing)
	}
	SetRouting("a", "ordered")
	if p, _ := Find("a"); p.Routing != "" {
		t.Fatal(p.Routing)
	}
}

func TestCodexClientVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := codexCLIVersion
	t.Cleanup(func() { codexCLIVersion = old })

	codexCLIVersion = func() string { return "0.155.1" }
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("no cache file: %s", got)
	}
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.154.0"}`), 0o644)
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("cli newer: %s", got)
	}
	codexCLIVersion = func() string { return "" }
	if got := codexClientVersionNow(); got != "0.154.0" {
		t.Fatalf("no cli: %s", got)
	}
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.9.0"}`), 0o644)
	if got := codexClientVersionNow(); got != codexClientVersion {
		t.Fatalf("built-in is newest: %s", got)
	}
	if compareVersions("0.155.1", "0.99.9") <= 0 || compareVersions("1.0", "1.0.0") != 0 {
		t.Fatal("compareVersions")
	}
}
