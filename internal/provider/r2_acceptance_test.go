package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// R2 acceptance tests for routing groups, model keys, and the Codex client
// version. Each test isolates the config, cache and home directories.

func r2Home(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func r2Save(t *testing.T, p Provider) {
	t.Helper()
	if err := Save(p); err != nil {
		t.Fatalf("Save(%s): %v", p.ID, err)
	}
}

// r2Two saves a offering "m" and b offering "vendor/m".
func r2Two(t *testing.T) {
	t.Helper()
	r2Save(t, Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1", Models: []string{"m"}})
	r2Save(t, Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1", Models: []string{"vendor/m"}})
}

func r2IDs() []string {
	var out []string
	for _, e := range Catalog() {
		out = append(out, e.ID)
	}
	return out
}

func r2Refs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Ref())
	}
	return out
}

// Acceptance 1 (catalog part): providers' models first, then the discovered
// group, which is marked as a group.
func TestR2CatalogListsGroupAfterProviders(t *testing.T) {
	r2Home(t)
	r2Two(t)
	ids := r2IDs()
	ia, ib, ig := slices.Index(ids, "a/m"), slices.Index(ids, "b/vendor/m"), slices.Index(ids, "group/auto-m")
	if ia < 0 || ib < 0 || ig < 0 {
		t.Fatalf("catalog ids %v lack a/m, b/vendor/m or group/auto-m", ids)
	}
	if ig < ia || ig < ib {
		t.Fatalf("group must come after the providers' models: %v", ids)
	}
	for _, e := range Catalog() {
		if e.ID == "group/auto-m" && !e.Group {
			t.Fatalf("group/auto-m entry not marked Group: %+v", e)
		}
		if e.ID == "a/m" && e.Group {
			t.Fatalf("provider model marked as group: %+v", e)
		}
	}
}

// Acceptance 2 (provider part): a saved user group keeps its member order and
// is listed as group/mine.
func TestR2SavedGroupKeepsMemberOrder(t *testing.T) {
	r2Home(t)
	r2Two(t)
	r2Save(t, Provider{ID: "c", Name: "C", Key: "kc", Chat: "https://c.example/v1", Models: []string{"only-a"}})
	if err := SaveGroup(Group{ID: "mine", Members: []string{"b/vendor/m", "c/only-a"}, Routing: Ordered}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok || g.ID != "mine" || g.Routing != Ordered {
		t.Fatalf("FindGroup: %+v ok=%v", g, ok)
	}
	if !slices.Equal(r2Refs(ms), []string{"b/vendor/m", "c/only-a"}) {
		t.Fatalf("members %v", r2Refs(ms))
	}
	if !slices.Contains(r2IDs(), "group/mine") {
		t.Fatalf("group/mine missing from catalog: %v", r2IDs())
	}
}

// Acceptance 3: deleting the discovered group hides it; ShowGroup brings it
// back. Deleting a group that does not exist is an error.
func TestR2DeleteAndShowDiscoveredGroup(t *testing.T) {
	r2Home(t)
	r2Two(t)
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatalf("DeleteGroup(auto-m): %v", err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("hidden group is still found")
	}
	if slices.Contains(r2IDs(), "group/auto-m") {
		t.Fatal("hidden group still in the catalog")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatalf("ShowGroup(auto-m): %v", err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("shown group: ok=%v members=%v", ok, r2Refs(ms))
	}
	if !slices.Contains(r2IDs(), "group/auto-m") {
		t.Fatal("shown group missing from the catalog")
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Fatal("deleting a missing group should fail")
	}
}

// Acceptance 4: an empty group is an error; a group member that is a group
// is ignored; a provider id of "group" is refused.
func TestR2SaveGroupRules(t *testing.T) {
	r2Home(t)
	r2Two(t)
	err := SaveGroup(Group{ID: "empty", Members: nil})
	if err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Fatalf("empty group error: %v", err)
	}
	if err := SaveGroup(Group{ID: "nested", Members: []string{"group/auto-m"}}); err == nil {
		t.Fatal("a group whose only member is a group should fail")
	}
	if err := SaveGroup(Group{ID: "mixed", Members: []string{"group/auto-m", "a/m"}}); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("mixed"); !ok || !slices.Equal(r2Refs(ms), []string{"a/m"}) {
		t.Fatalf("nested group should be ignored, members %v ok=%v", r2Refs(ms), ok)
	}
	if err := Save(Provider{ID: "group", Name: "G", Key: "kg", Chat: "https://g.example/v1", Models: []string{"m"}}); err == nil {
		t.Fatal(`provider id "group" should be refused`)
	}
}

// Invalid routing and affinity values fall back to the defaults.
func TestR2InvalidRoutingAndAffinityFallBack(t *testing.T) {
	r2Home(t)
	r2Two(t)
	if err := SaveGroup(Group{ID: "odd", Members: []string{"a/m"}, Routing: "sideways", Affinity: "forever"}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("odd"); !ok || g.Routing != "" || g.Affinity != "" {
		t.Fatalf("invalid values kept: %+v ok=%v", g, ok)
	}
	if err := SetAffinity("a", "forever"); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("a"); err != nil || p.Affinity != "" {
		t.Fatalf("provider affinity: %+v err=%v", p, err)
	}
	if err := SetAffinity("a", AffinitySession); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("a"); err != nil || p.Affinity != AffinitySession {
		t.Fatalf("provider affinity not set: %+v err=%v", p, err)
	}
}

// Acceptance 8: spellings of one model share a normalized key; the colon
// variant and the non-version dot are left alone.
func TestR2NormalizeModelKeySpellings(t *testing.T) {
	for _, m := range []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801", "claude-opus-5.5-20260801"} {
		if got := NormalizeModelKey(m); got != "claude-opus-5-5" {
			t.Errorf("NormalizeModelKey(%q) = %q, want claude-opus-5-5", m, got)
		}
	}
	if got := NormalizeModelKey("claude-opus-5-5:batch"); got != "claude-opus-5-5:batch" {
		t.Errorf("colon variant rewritten: %q", got)
	}
	if got := NormalizeModelKey("claude-opus-5-5:batch"); got == NormalizeModelKey("claude-opus-5-5") {
		t.Errorf("colon variant merged into the base model")
	}
	if got := NormalizeModelKey("v1.beta"); got != "v1.beta" {
		t.Errorf("NormalizeModelKey(v1.beta) = %q", got)
	}
}

// Acceptance 8 (discovery part): four spellings across two providers make one
// auto group named "Claude Opus 5.5", with every spelling as a member.
func TestR2DiscoveryMergesSpellings(t *testing.T) {
	r2Home(t)
	r2Save(t, Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1",
		Models: []string{"claude-opus-5-5", "Claude-Opus-5.5"}})
	r2Save(t, Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1",
		Models: []string{"anthropic/claude-opus-5.5", "claude-opus-5-5-20260801"}})
	g, ms, ok := FindGroup("auto-claude-opus-5-5")
	if !ok || !g.Auto {
		t.Fatalf("auto group missing: %+v ok=%v", g, ok)
	}
	if g.Name != "Claude Opus 5.5" {
		t.Fatalf("group name %q, want Claude Opus 5.5", g.Name)
	}
	want := []string{"a/claude-opus-5-5", "a/Claude-Opus-5.5", "b/anthropic/claude-opus-5.5", "b/claude-opus-5-5-20260801"}
	if !slices.Equal(r2Refs(ms), want) {
		t.Fatalf("members %v, want %v", r2Refs(ms), want)
	}
	n := 0
	for _, e := range Catalog() {
		if e.ID == "group/auto-claude-opus-5-5" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one auto group entry, got %d", n)
	}
}

// Acceptance 10: the Codex client version is the newest of the installed CLI,
// the Codex cache and the built-in one.
func TestR2CodexClientVersionNewest(t *testing.T) {
	r2Home(t)
	dir := filepath.Join(os.Getenv("HOME"), ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"client_version":"0.154.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := codexVersionCLI
	t.Cleanup(func() { codexVersionCLI = old })

	codexVersionCLI = func() (string, bool) { return "0.155.1", true }
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("with CLI 0.155.1 and cache 0.154.0: client version %q", got)
	}
	codexVersionCLI = func() (string, bool) { return "", false }
	if got := codexClientVersionNow(); got != "0.154.0" {
		t.Fatalf("without CLI: client version %q, want 0.154.0", got)
	}
}
