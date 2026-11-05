package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// groupEnv isolates the config and the caches, with an empty models.dev
// catalog so nothing is fetched.
func groupEnv(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	if err := os.MkdirAll(filepath.Join(cache, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// twoVendors has vendor a serving claude-opus-5-5 and b serving the same
// model under another spelling, plus a model of a's alone.
func twoVendors(t *testing.T) {
	t.Helper()
	if err := Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("a", "https://a.example/v1", []catalog.Model{
		{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
		{ID: "only-a", Name: "Only A"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("b", "https://b.example/v1", []catalog.Model{
		{ID: "anthropic/claude-opus-5.5", Name: "anthropic/claude-opus-5.5"},
	}); err != nil {
		t.Fatal(err)
	}
}

func catalogIDs() []string {
	var out []string
	for _, e := range Catalog() {
		out = append(out, e.ID)
	}
	return out
}

func TestAutoGroupFound(t *testing.T) {
	groupEnv(t)
	twoVendors(t)

	ids := catalogIDs()
	want := []string{"a/claude-opus-5-5", "a/only-a", "b/anthropic/claude-opus-5.5", "group/auto-claude-opus-5-5"}
	if !slices.Equal(ids, want) {
		t.Fatalf("catalog = %v, want %v", ids, want)
	}
	var auto Entry
	for _, e := range Catalog() {
		if e.Group != "" {
			auto = e
		}
	}
	if auto.Name != "Claude Opus 5.5" {
		t.Errorf("auto group name = %q, want the display name", auto.Name)
	}
	if auto.Provider.ID != "group" {
		t.Errorf("group entry provider = %q, want group", auto.Provider.ID)
	}
	g, ms, ok := FindGroup("group/auto-claude-opus-5-5")
	if !ok || !g.Auto {
		t.Fatalf("FindGroup auto = %+v ok=%v", g, ok)
	}
	if len(ms) != 2 || ms[0].ID() != "a/claude-opus-5-5" || ms[1].ID() != "b/anthropic/claude-opus-5.5" {
		t.Errorf("members = %v", ms)
	}
}

func TestDifferentNamesNeverGroup(t *testing.T) {
	groupEnv(t)
	twoVendors(t)
	// only-a has no twin at b, so it is not grouped; nothing else is
	for _, id := range catalogIDs() {
		if strings.Contains(id, "only-a") && strings.HasPrefix(id, "group/") {
			t.Fatalf("grouped a model with no twin: %s", id)
		}
	}
}

func TestSaveGroupRules(t *testing.T) {
	groupEnv(t)
	twoVendors(t)

	if err := SaveGroup(Group{Name: "Empty"}); err == nil || err.Error() != "组里至少要有一个模型" {
		t.Errorf("empty group error = %v", err)
	}
	if err := SaveGroup(Group{Name: "Only nest", Members: []string{"group/auto-claude-opus-5-5"}}); err == nil {
		t.Errorf("a group of only groups should be refused")
	}
	if err := Save(Provider{ID: "group", Name: "G", Key: "k", Chat: "https://g.example/v1"}); err == nil {
		t.Errorf("provider id group should be refused")
	}

	// a group named inside a group is ignored; the rest is kept, in order
	if err := SaveGroup(Group{Name: "Mine", Members: []string{"group/auto-claude-opus-5-5", "b/anthropic/claude-opus-5.5", "a/only-a", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	g, _, ok := FindGroup("mine")
	if !ok {
		t.Fatal("saved group not found")
	}
	if g.ID != "mine" || g.Name != "Mine" {
		t.Errorf("group = %+v", g)
	}
	if want := []string{"b/anthropic/claude-opus-5.5", "a/only-a"}; !slices.Equal(g.Members, want) {
		t.Errorf("members = %v, want %v", g.Members, want)
	}
}

func TestSaveGroupIDAndNameDefaults(t *testing.T) {
	groupEnv(t)
	twoVendors(t)
	if err := SaveGroup(Group{Name: "My Opus Pool", Members: []string{"a/only-a"}, Routing: "bogus", Affinity: "nope"}); err != nil {
		t.Fatal(err)
	}
	g, _, ok := FindGroup("my-opus-pool")
	if !ok {
		t.Fatal("id not derived from the name")
	}
	if g.Routing != "" || g.Affinity != "" {
		t.Errorf("invalid routing/affinity should fall back to default: %+v", g)
	}
	if err := SaveGroup(Group{ID: "named-by-id", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("named-by-id"); g.Name != "named-by-id" {
		t.Errorf("name should default to id, got %q", g.Name)
	}
	if err := SaveGroup(Group{ID: "Bad Id", Members: []string{"a/only-a"}}); err == nil {
		t.Errorf("an invalid id should be refused")
	}
}

func TestDeleteAndShowAutoGroup(t *testing.T) {
	groupEnv(t)
	twoVendors(t)

	if err := DeleteGroup("auto-claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(catalogIDs(), "group/auto-claude-opus-5-5") {
		t.Fatal("hidden group still listed")
	}
	if _, _, ok := FindGroup("group/auto-claude-opus-5-5"); ok {
		t.Fatal("hidden group still found")
	}
	if err := ShowGroup("auto-claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(catalogIDs(), "group/auto-claude-opus-5-5") {
		t.Fatal("shown group not listed again")
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Error("deleting an unknown group should fail")
	}
	if err := ShowGroup("no-such-group"); err == nil {
		t.Error("showing an unknown group should fail")
	}
}

func TestUserOwnsAutoGroupOnceSaved(t *testing.T) {
	groupEnv(t)
	twoVendors(t)
	if err := SaveGroup(Group{ID: "auto-claude-opus-5-5", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-claude-opus-5-5")
	if !ok || g.Auto || len(ms) != 1 || ms[0].ID() != "a/only-a" {
		t.Fatalf("saved auto id should be the user's: %+v %v %v", g, ms, ok)
	}
}

func TestFindGroupSkipsUnreadyAndDuplicates(t *testing.T) {
	groupEnv(t)
	twoVendors(t)
	if err := SaveGroup(Group{ID: "dup", Members: []string{"a/only-a", "a/only-a", "ghost/x", "b/anthropic/claude-opus-5.5"}}); err != nil {
		t.Fatal(err)
	}
	_, ms, ok := FindGroup("dup")
	if !ok || len(ms) != 2 {
		t.Fatalf("members = %v ok=%v, want the two ready ones", ms, ok)
	}
	if err := SaveGroup(Group{ID: "ghosts", Members: []string{"ghost/x"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("ghosts"); ok {
		t.Error("a group with no member ready should not be ok")
	}
}

func TestSetAffinity(t *testing.T) {
	groupEnv(t)
	twoVendors(t)
	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if p.Affinity != "session" {
		t.Errorf("affinity = %q, want session", p.Affinity)
	}
	if err := SetAffinity("a", "sometimes"); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("a"); p.Affinity != "" {
		t.Errorf("invalid affinity should fall back to automatic, got %q", p.Affinity)
	}
	if err := SetAffinity("nobody", "off"); err == nil {
		t.Error("an unknown provider should fail")
	}
}

func TestCatalogGroupsAfterProviders(t *testing.T) {
	groupEnv(t)
	twoVendors(t)
	if err := SaveGroup(Group{ID: "mine", Members: []string{"b/anthropic/claude-opus-5.5", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	ids := catalogIDs()
	// providers first, then the groups in the order they were found
	want := []string{"a/claude-opus-5-5", "a/only-a", "b/anthropic/claude-opus-5.5", "group/mine", "group/auto-claude-opus-5-5"}
	if !slices.Equal(ids, want) {
		t.Fatalf("catalog = %v, want %v", ids, want)
	}
}

func TestDeleteGroupTwiceFails(t *testing.T) {
	groupEnv(t)
	twoVendors(t)

	if err := DeleteGroup("auto-claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("auto-claude-opus-5-5"); err == nil {
		t.Error("deleting an already hidden automatic group should fail")
	}
	if err := ShowGroup("auto-claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("auto-claude-opus-5-5"); err != nil {
		t.Errorf("deleting a shown automatic group again should succeed: %v", err)
	}
}
