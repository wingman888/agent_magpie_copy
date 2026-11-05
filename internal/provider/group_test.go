package provider

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// groupsEnv isolates the config and cache, and returns the fake home.
func groupsEnv(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// addProvider saves a key provider whose list is models.
func addProvider(t *testing.T, id string, models ...catalog.Model) {
	t.Helper()
	base := "https://" + id + ".example/v1"
	if err := Save(Provider{ID: id, Name: id, Chat: base, Key: "sk-" + id}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(id, base, models); err != nil {
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

func TestModelKey(t *testing.T) {
	same := []string{
		"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801", "anthropic/Claude-Opus-5.5-20260801",
	}
	for _, m := range same {
		if k := modelKey(m); k != "claude-opus-5-5" {
			t.Errorf("modelKey(%q) = %q, want claude-opus-5-5", m, k)
		}
	}
	if k := modelKey("claude-opus-5.5:batch"); k != "claude-opus-5-5:batch" || k == modelKey("claude-opus-5.5") {
		t.Errorf(":batch variant = %q; it must stay a model of its own", k)
	}
	if k := modelKey("v1.beta"); k != "v1.beta" {
		t.Errorf("modelKey(v1.beta) = %q, the dot is not a version", k)
	}
	if k := modelKey("gpt-5.5.1"); k != "gpt-5-5-1" {
		t.Errorf("modelKey(gpt-5.5.1) = %q", k)
	}
}

func TestAutoGroupAcrossProvidersAndCatalogOrder(t *testing.T) {
	groupsEnv(t)
	addProvider(t, "a", catalog.Model{ID: "m", Name: "m"})
	addProvider(t, "b", catalog.Model{ID: "vendor/m", Name: "Vendor: M"}, catalog.Model{ID: "v1.beta"})

	if got, want := ids(Catalog()), []string{"a/m", "b/vendor/m", "b/v1.beta", "group/auto-m"}; !slices.Equal(got, want) {
		t.Fatalf("catalog = %v, want %v", got, want)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || len(ms) != 2 || g.Name != "Vendor: M" || !g.Auto {
		t.Fatalf("auto-m = %+v, %d members, %v", g, len(ms), ok)
	}
	if ms[0].Provider.ID != "a" || ms[1].Provider.ID != "b" || ms[1].Model != "vendor/m" {
		t.Fatalf("members out of provider order: %+v", ms)
	}
}

func TestAutoGroupNameIsTheDisplayName(t *testing.T) {
	groupsEnv(t)
	addProvider(t, "a", catalog.Model{ID: "claude-opus-5-5", Name: "claude-opus-5-5"})
	addProvider(t, "b", catalog.Model{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"},
		catalog.Model{ID: "claude-opus-5-5:batch", Name: "batch"}, catalog.Model{ID: "v1-beta", Name: "v1 beta"})
	if err := catalog.SaveLive("a", "https://a.example/v1", []catalog.Model{{ID: "claude-opus-5-5"}, {ID: "v1.beta"}}); err != nil {
		t.Fatal(err)
	}
	gs := Groups()
	var names []string
	for _, g := range gs {
		names = append(names, g.ID+"="+g.Name)
	}
	want := []string{"auto-claude-opus-5-5=Claude Opus 5.5"}
	if !slices.Equal(names, want) {
		t.Fatalf("groups = %v, want %v (no :batch, v1.beta or v1-beta merge)", names, want)
	}
	if _, ms, _ := FindGroup("auto-claude-opus-5-5"); len(ms) != 2 {
		t.Fatalf("members = %+v", ms)
	}
}

func TestSaveDeleteShowGroup(t *testing.T) {
	groupsEnv(t)
	addProvider(t, "a", catalog.Model{ID: "m"}, catalog.Model{ID: "only-a"})
	addProvider(t, "b", catalog.Model{ID: "vendor/m"})

	if err := SaveGroup(Group{Name: "Mine", Members: []string{"b/vendor/m", "a/only-a", "group/auto-m", "b/vendor/m"}, Routing: "order"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok || g.ID != "mine" || g.Name != "Mine" || g.Routing != "order" || g.Auto {
		t.Fatalf("saved group = %+v, %v", g, ok)
	}
	if !slices.Equal(g.Members, []string{"b/vendor/m", "a/only-a"}) {
		t.Fatalf("members = %v; a group in a group and a repeat are left out", g.Members)
	}
	if len(ms) != 2 || ms[0].Provider.ID != "b" || ms[1].Model != "only-a" {
		t.Fatalf("resolved = %+v", ms)
	}
	if err := SaveGroup(Group{ID: "x", Members: []string{"group/mine"}}); err == nil || err.Error() != "组里至少要有一个模型" {
		t.Fatalf("only a group as member: %v", err)
	}
	if err := SaveGroup(Group{ID: "x", Routing: "ordered"}); err == nil {
		t.Fatal("empty members must be an error")
	}
	if err := SaveGroup(Group{ID: "x", Members: []string{"a/m"}, Routing: "ordered", Affinity: "always"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("x"); g.Routing != "" || g.Affinity != "" {
		t.Fatalf("invalid routing/affinity kept: %+v", g)
	}
	if err := SaveGroup(Group{ID: "y", Members: []string{"a/m"}, Routing: "usage"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("y"); g.Routing != LeastUsed {
		t.Fatalf("usage (older spelling) = %q, want least-used", g.Routing)
	}

	// the auto group: hidden on delete, back on show
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("auto-m should be found")
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Fatal("hidden auto group is still found")
	}
	if slices.Contains(ids(Catalog()), "group/auto-m") {
		t.Fatal("hidden auto group still in the catalog")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("shown auto-m = %v, %d members", ok, len(ms))
	}
	if err := DeleteGroup("nope"); err == nil {
		t.Fatal("deleting an unknown group must fail")
	}
	// a group the user made goes for good
	if err := DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("mine"); ok {
		t.Fatal("deleted group still found")
	}
}

func TestSavingAnAutoGroupMakesItTheUsers(t *testing.T) {
	groupsEnv(t)
	addProvider(t, "a", catalog.Model{ID: "m"})
	addProvider(t, "b", catalog.Model{ID: "m"})
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-m")
	if !ok || g.Auto || len(ms) != 1 {
		t.Fatalf("user's auto-m = %+v, %d members", g, len(ms))
	}
	// the user deleting it hides it: the providers would find it again
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("deleted auto-m still found")
	}
}

func TestProviderIDGroupIsRefused(t *testing.T) {
	groupsEnv(t)
	if err := Save(Provider{ID: "group", Name: "group", Chat: "https://x.example/v1", Key: "k"}); err == nil {
		t.Fatal("a provider called group must be refused")
	}
}

func TestSetAffinityAndRouting(t *testing.T) {
	groupsEnv(t)
	addProvider(t, "a", catalog.Model{ID: "m"})
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "session" {
		t.Fatalf("affinity = %q", p.Affinity)
	}
	if err := SetAffinity("a", "sometimes"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "" {
		t.Fatalf("invalid affinity kept: %q", p.Affinity)
	}
	if err := SetAffinity("nope", "turn"); err == nil {
		t.Fatal("unknown provider must fail")
	}
	if err := SetRouting("a", "ordered"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Routing != "" {
		t.Fatalf("ordered is not a routing: %q", p.Routing)
	}
}

func TestLegacyFileStillLoads(t *testing.T) {
	home := groupsEnv(t)
	addProvider(t, "a", catalog.Model{ID: "m"})
	// a providers.json written before groups existed still reads
	b := []byte(`{"providers":[{"id":"a","name":"a","chat":"https://a.example/v1","key":"sk-a","routing":"usage"}]}`)
	if err := os.WriteFile(filepath.Join(home, ".config", "magpie", "providers.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("a"); err != nil || p.Routing != LeastUsed {
		t.Fatalf("legacy provider = %+v, %v", p, err)
	}
}

func TestCodexClientVersionIsTheNewest(t *testing.T) {
	home := groupsEnv(t)
	old := codexVersionCommand
	t.Cleanup(func() { codexVersionCommand = old; resetCodexVersion() })
	cli := func(out string, err error) func() (string, error) {
		return func() (string, error) { return out, err }
	}

	codexVersionCommand = cli("codex-cli 0.155.1\n", nil)
	resetCodexVersion()
	if v := codexVersionNow(); v != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache none: asked with %s", v)
	}

	codexVersionCommand = cli("", errors.New("no codex"))
	resetCodexVersion()
	if v := codexVersionNow(); v != codexClientVersion {
		t.Fatalf("no CLI, no cache: asked with %s", v)
	}

	// the cache records 0.156.0, a newer one than the CLI's 0.155.1
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.156.0"}`), 0o644)
	codexVersionCommand = cli("codex-cli 0.155.1", nil)
	resetCodexVersion()
	if v := codexVersionNow(); v != "0.156.0" {
		t.Fatalf("cache 0.156.0 over CLI 0.155.1: asked with %s", v)
	}
}

func resetCodexVersion() {
	codexVersion.Lock()
	codexVersion.v, codexVersion.at = "", time.Time{}
	codexVersion.Unlock()
}
