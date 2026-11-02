package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func grpHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func grpIDs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func TestGrpCanonicalModel(t *testing.T) {
	same := []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801"}
	for _, s := range same {
		if got := CanonicalModel(s); got != "claude-opus-5-5" {
			t.Errorf("%q → %q", s, got)
		}
	}
	if got := CanonicalModel("claude-opus-5-5:batch"); got != "claude-opus-5-5:batch" {
		t.Errorf("variant %q", got)
	}
	if got := CanonicalModel("gemini-v1.beta"); got != "gemini-v1.beta" {
		t.Errorf("v1.beta %q", got)
	}
	if got := CanonicalModel("x-1.2.3"); got != "x-1-2-3" {
		t.Errorf("1.2.3 %q", got)
	}
}

func TestGrpAutoGroupInCatalog(t *testing.T) {
	grpHome(t)
	if err := Save(Provider{ID: "a", Key: "k", Chat: "https://a.example/v1", Models: []string{"m", "only-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Key: "k", Chat: "https://b.example/v1", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	got := grpIDs(Catalog())
	want := []string{"a/m", "a/only-a", "b/vendor/m", "group/auto-m"}
	if !slices.Equal(got, want) {
		t.Fatalf("catalog %v", got)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || !g.Auto || len(ms) != 2 || ms[0].Ref != "a/m" || ms[1].Ref != "b/vendor/m" {
		t.Fatalf("group %+v %+v %v", g, ms, ok)
	}
	if p, m, ok := Resolve("group/auto-m"); !ok || p.ID != "a" || m != "m" {
		t.Fatalf("resolve %v %v %v", p.ID, m, ok)
	}
	// nothing stored for a found group
	if b, _ := os.ReadFile(Path()); strings.Contains(string(b), "auto-m") {
		t.Fatalf("stored: %s", b)
	}
	// the CLI lists groups first
	_ = got

	// hidden, then shown again
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Fatal("hidden group still found")
	}
	if _, _, ok := Resolve("group/auto-m"); ok {
		t.Fatal("hidden group resolves")
	}
	if slices.Contains(grpIDs(Catalog()), "group/auto-m") {
		t.Fatal("hidden group listed")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); !ok {
		t.Fatal("shown group missing")
	}
	if err := DeleteGroup("nope"); err == nil {
		t.Fatal("deleting a missing group")
	}

	// changed, it is the user's
	if err := SaveGroup(Group{ID: "auto-m", Name: "Mine M", Members: []string{"b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok = FindGroup("group/auto-m")
	if !ok || g.Auto || g.Name != "Mine M" || len(ms) != 1 {
		t.Fatalf("user group %+v %+v", g, ms)
	}
}

func TestGrpSaveGroupRules(t *testing.T) {
	grpHome(t)
	Save(Provider{ID: "a", Key: "k", Chat: "https://a.example/v1", Models: []string{"only-a"}})
	Save(Provider{ID: "b", Key: "k", Chat: "https://b.example/v1", Models: []string{"vendor/m"}})
	if err := SaveGroup(Group{Name: "Empty"}); err == nil || err.Error() != "组里至少要有一个模型" {
		t.Fatalf("empty: %v", err)
	}
	if err := SaveGroup(Group{Name: "Nested", Members: []string{"group/x"}}); err == nil {
		t.Fatal("only a nested group")
	}
	if err := Save(Provider{ID: "group", Key: "k", Chat: "https://g.example/v1"}); err == nil {
		t.Fatal("provider id group")
	}
	err := SaveGroup(Group{Name: "Mine", Members: []string{"b/vendor/m", "group/other", "a/only-a", "b/vendor/m"}, Routing: "order", Affinity: "bogus"})
	if err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok || g.ID != "mine" || g.Name != "Mine" || g.Routing != Ordered || g.Affinity != "" {
		t.Fatalf("group %+v", g)
	}
	if !slices.Equal(g.Members, []string{"b/vendor/m", "a/only-a"}) || len(ms) != 2 || ms[0].Provider.ID != "b" || ms[0].Model != "vendor/m" {
		t.Fatalf("members %v %+v", g.Members, ms)
	}
	SaveGroup(Group{ID: "x", Members: []string{"a/only-a"}, Routing: "ordered"})
	if g, _, _ := FindGroup("x"); g.Name != "x" || g.Routing != "" {
		t.Fatalf("defaults %+v", g)
	}
	if err := DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/mine"); ok {
		t.Fatal("deleted")
	}
	// a group whose members are not ready is found, with none
	SaveGroup(Group{ID: "ghost", Members: []string{"zz/m"}})
	if _, ms, ok := FindGroup("group/ghost"); !ok || len(ms) != 0 {
		t.Fatal("ghost")
	}
	if slices.Contains(grpIDs(Catalog()), "group/ghost") {
		t.Fatal("ghost listed")
	}
}

func TestGrpAffinityAndRouting(t *testing.T) {
	grpHome(t)
	Save(Provider{ID: "a", Key: "k", Chat: "https://a.example/v1", Routing: "usage"})
	p, _ := Find("a")
	if p.Routing != LeastUsed || LeastUsed != "least-used" {
		t.Fatalf("legacy routing %q", p.Routing)
	}
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "session" {
		t.Fatalf("affinity %q", p.Affinity)
	}
	if err := SetAffinity("a", "bogus"); err == nil {
		t.Fatal("bogus affinity")
	}
	SetRouting("a", "nonsense")
	if p, _ := Find("a"); p.Routing != "" {
		t.Fatalf("routing %q", p.Routing)
	}
}

func TestGrpAutoGroupNameAndEfforts(t *testing.T) {
	grpHome(t)
	Save(Provider{ID: "plain", Key: "k", Chat: "https://p.example/v1", Models: []string{"claude-opus-5-5"}})
	Save(Provider{ID: "router", Key: "k", Chat: "https://r.example/v1", Models: []string{"anthropic/claude-opus-5.5", "claude-opus-5-5:batch", "x-v1.beta"}})
	Save(Provider{ID: "third", Key: "k", Chat: "https://t.example/v1", Models: []string{"Claude-Opus-5.5", "claude-opus-5-5-20260801"}})
	catalog.SaveLive("router", "https://r.example/v1", []catalog.Model{
		{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5", Efforts: []string{"low", "high"}},
		{ID: "claude-opus-5-5:batch", Name: "claude-opus-5-5:batch"},
		{ID: "x-v1.beta"},
	})
	var gs []Group
	for _, g := range Groups() {
		gs = append(gs, g)
	}
	if len(gs) != 1 || gs[0].ID != "auto-claude-opus-5-5" || gs[0].Name != "Claude Opus 5.5" {
		t.Fatalf("groups %+v", gs)
	}
	if !slices.Equal(gs[0].Members, []string{"plain/claude-opus-5-5", "router/anthropic/claude-opus-5.5", "third/Claude-Opus-5.5"}) {
		t.Fatalf("members %v", gs[0].Members)
	}
	for _, e := range Catalog() {
		if e.IsGroup() && len(e.Efforts) != 0 {
			t.Fatalf("efforts not every member has: %v", e.Efforts)
		}
	}
}

func TestGrpCodexAskVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.154.0"}`), 0o644)
	t.Setenv("PATH", t.TempDir())
	if v := codexAskVersion(); v != "0.154.0" {
		t.Fatalf("no CLI: %q", v)
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho codex-cli 0.155.1\n"), 0o755)
	t.Setenv("PATH", bin)
	if v := codexAskVersion(); v != "0.155.1" {
		t.Fatalf("CLI: %q", v)
	}
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.160.0"}`), 0o644)
	if v := codexAskVersion(); v != "0.160.0" {
		t.Fatalf("newer cache: %q", v)
	}
	if newestVersion("", "0.9.0", "0.10.0-alpha", "0.10.0") != "0.10.0" {
		t.Fatal("compare")
	}
}

func TestGrpShowMissingAndHashedID(t *testing.T) {
	grpHome(t)
	Save(Provider{ID: "a", Key: "k", Chat: "https://a.example/v1", Models: []string{"only-a"}})
	if err := ShowGroup("none-such"); err == nil {
		t.Fatal("showing a missing group")
	}
	if err := SaveGroup(Group{Name: "我的组", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	gs := Groups()
	if len(gs) != 1 || !strings.HasPrefix(gs[0].ID, "g-") || gs[0].ID != Slug(gs[0].ID) || gs[0].Name != "我的组" {
		t.Fatalf("groups %+v", gs)
	}
	if err := ShowGroup(gs[0].ID); err != nil {
		t.Fatalf("showing a shown group: %v", err)
	}
	if groupSlug("我的组") != gs[0].ID {
		t.Fatal("hash not stable")
	}
}

// A bare model id served by several providers still resolves as before
// groups: to the first provider that exposes it, never to its auto group.
func TestGrpBareIDResolvesAsBefore(t *testing.T) {
	grpHome(t)
	Save(Provider{ID: "a", Key: "k", Chat: "https://a.example/v1", Models: []string{"m"}})
	Save(Provider{ID: "b", Key: "k", Chat: "https://b.example/v1", Models: []string{"m"}})
	if !slices.Contains(grpIDs(Catalog()), "group/auto-m") {
		t.Fatalf("no auto group: %v", grpIDs(Catalog()))
	}
	p, m, ok := Resolve("m")
	if !ok || p.ID != "a" || m != "m" {
		t.Fatalf("bare id: %v %q %v", p.ID, m, ok)
	}
	if p, m, ok := Resolve("b/m"); !ok || p.ID != "b" || m != "m" {
		t.Fatalf("qualified: %v %q %v", p.ID, m, ok)
	}
}
