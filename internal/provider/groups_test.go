package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// groupHome isolates the config, cache and signed-in accounts for a test.
func groupHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// saveProvider configures a plain key+URL provider offering the given models.
func saveProvider(t *testing.T, id string, models ...string) {
	t.Helper()
	if err := Save(Provider{ID: id, Name: id, Chat: "https://" + id + ".example/v1", Key: "k-" + id, Models: models}); err != nil {
		t.Fatal(err)
	}
}

func memberRefs(ms []Member) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Provider+"/"+m.Model)
	}
	return out
}

func entryIDs(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	return out
}

func TestSaveGroupRules(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "only-a")

	if err := SaveGroup(Group{ID: "empty"}); err == nil || !strings.Contains(err.Error(), "组里至少要有一个模型") {
		t.Errorf("empty members: err = %v", err)
	}
	if err := SaveGroup(Group{ID: "nested", Members: []string{"group/mine", " "}}); err == nil {
		t.Error("a group with only a nested group as member must be refused")
	}

	if err := Save(Provider{ID: "group", Name: "group", Chat: "https://g.example/v1", Key: "k"}); err == nil {
		t.Error(`provider id "group" must be refused`)
	}

	// The id comes from the name; the name defaults to the id; the group
	// inside a group is ignored; a repeated member counts once.
	if err := SaveGroup(Group{Name: "My Mix", Members: []string{"a/only-a", "group/x", "a/only-a"}, Routing: "order", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/my-mix")
	if !ok {
		t.Fatal("saved group not found")
	}
	if g.Name != "My Mix" || g.Routing != "order" || g.Affinity != "" || g.Auto {
		t.Errorf("group = %+v", g)
	}
	if !slices.Equal(g.Members, []string{"a/only-a"}) {
		t.Errorf("members kept = %v", g.Members)
	}
	if len(ms) != 1 || ms[0].Provider != "a" || ms[0].Model != "only-a" {
		t.Errorf("resolved members = %+v", ms)
	}

	// Saving with the same id replaces it.
	if err := SaveGroup(Group{ID: "my-mix", Members: []string{"a/only-a"}, Routing: "bogus"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("my-mix"); g.Routing != "" || g.Name != "my-mix" {
		t.Errorf("replaced group = %+v", g)
	}
}

// A model the same in two providers, written alike, is one automatic group;
// the vendor prefix and a dot instead of a hyphen are not a different model.
func TestAutoGroupDiscovery(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "m")
	saveProvider(t, "b", "vendor/m")
	saveProvider(t, "c", "other")

	ids := entryIDs(Catalog())
	want := []string{"a/m", "b/vendor/m", "c/other", "group/auto-m"}
	if !slices.Equal(ids, want) {
		t.Fatalf("catalog ids = %v, want %v", ids, want)
	}
	var grp Entry
	for _, e := range Catalog() {
		if e.Group != nil {
			grp = e
		}
	}
	if grp.Group == nil || !grp.Group.Auto || !slices.Equal(grp.Group.Members, []string{"a/m", "b/vendor/m"}) {
		t.Errorf("auto group = %+v", grp.Group)
	}
	if len(grp.Members) != 2 {
		t.Errorf("usable members = %+v", grp.Members)
	}
}

// Same model, four spellings, one group named for its official display name.
func TestAutoGroupSameModelSpellings(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "claude-opus-5-5")
	saveProvider(t, "b", "anthropic/claude-opus-5.5")
	saveProvider(t, "c", "Claude-Opus-5.5")
	saveProvider(t, "d", "claude-opus-5-5-20260801")
	if err := catalog.SaveLive("b", "https://b.example/v1", []catalog.Model{{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"}}); err != nil {
		t.Fatal(err)
	}

	var groups []Entry
	for _, e := range Catalog() {
		if e.Group != nil {
			groups = append(groups, e)
		}
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %v", entryIDs(groups))
	}
	if groups[0].ID != "group/auto-claude-opus-5-5" || groups[0].Name != "Claude Opus 5.5" {
		t.Errorf("group = %q %q", groups[0].ID, groups[0].Name)
	}
	if len(groups[0].Members) != 4 {
		t.Errorf("members = %+v", groups[0].Members)
	}
}

// A colon variant and a dot that isn't between two numbers are other models,
// and models with different names never group on their own.
func TestAutoGroupKeepsDifferentModels(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "m:batch", "m", "v1.beta", "x")
	saveProvider(t, "b", "m", "v1.beta", "y")

	allowed := []string{"group/auto-m", "group/auto-v1-beta"}
	for _, e := range Catalog() {
		if e.Group != nil && !slices.Contains(allowed, e.ID) {
			t.Errorf("unexpected group %q", e.ID)
		}
	}
	if _, ms, ok := FindGroup("auto-v1-beta"); !ok || len(ms) != 2 {
		t.Errorf("v1.beta group: ok=%v members=%+v", ok, ms)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || !slices.Equal(memberRefs(ms), []string{"a/m", "b/m"}) {
		t.Errorf("m should group with m across providers: ok=%v members=%+v", ok, ms)
	}
	// m:batch is offered by one provider only, and is not m.
	if _, _, ok := FindGroup("auto-m-batch"); ok {
		t.Error("m:batch must not group with anything")
	}
}

// A user's group with the id of an automatic one takes its place.
func TestUserGroupReplacesAuto(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "m")
	saveProvider(t, "b", "vendor/m")

	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/auto-m")
	if !ok || g.Auto || len(ms) != 1 || ms[0].Provider != "b" {
		t.Errorf("replaced: ok=%v group=%+v members=%+v", ok, g, ms)
	}
}

func TestDeleteAndShowAutoGroup(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "m")
	saveProvider(t, "b", "vendor/m")

	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("group/auto-m"); ok && len(ms) > 0 {
		t.Errorf("hidden group still usable: %+v", ms)
	}
	if slices.Contains(entryIDs(Catalog()), "group/auto-m") {
		t.Error("hidden group still in catalog")
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Errorf("hiding twice: %v", err)
	}
	if err := DeleteGroup("no-such"); err == nil {
		t.Error("deleting a missing group must fail")
	}

	if err := ShowGroup("group/auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || len(ms) != 2 {
		t.Errorf("shown again: ok=%v members=%+v", ok, ms)
	}
	if err := ShowGroup("no-such"); err == nil {
		t.Error("showing a group that was never hidden must fail")
	}
}

func TestDeleteUserGroup(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "only-a")
	if err := SaveGroup(Group{ID: "mine", Members: []string{"a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("mine"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("mine"); ok {
		t.Error("deleted user group still found")
	}
}

func TestSetAffinityPersists(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "m")

	if err := SetAffinity("a", "session"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("a")
	if err != nil || p.Affinity != "session" {
		t.Fatalf("affinity after save = %q, %v", p.Affinity, err)
	}
	b, err := os.ReadFile(Path())
	if err != nil || !strings.Contains(string(b), `"affinity": "session"`) {
		t.Errorf("providers.json lacks affinity: %v", err)
	}
	if err := SetAffinity("a", "sometimes"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "" {
		t.Errorf("invalid affinity kept as %q", p.Affinity)
	}
	if err := SetAffinity("nobody", "off"); err == nil {
		t.Error("unknown provider must fail")
	}
}

// A providers.json from before groups existed reads as it did.
func TestOldConfigWithoutGroups(t *testing.T) {
	home := groupHome(t)
	path := filepath.Join(home, ".config", "magpie", "providers.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"providers":[{"id":"a","name":"a","key":"k","chat":"https://a.example/v1","models":["m"]}]}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(Catalog()); !slices.Equal(got, []string{"a/m"}) {
		t.Errorf("catalog = %v", got)
	}
	if _, _, ok := FindGroup("group/nothing"); ok {
		t.Error("no group should be found")
	}
}

// The members' reasoning levels a group offers are those every member has.
func TestGroupEffortsAreShared(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "m")
	saveProvider(t, "b", "vendor/m")
	if err := catalog.SaveLive("a", "https://a.example/v1", []catalog.Model{{ID: "m", Efforts: []string{"low", "high"}}}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("b", "https://b.example/v1", []catalog.Model{{ID: "vendor/m", Efforts: []string{"high", "low", "max"}}}); err != nil {
		t.Fatal(err)
	}
	for _, e := range Catalog() {
		if e.Group != nil && !slices.Equal(e.Efforts, []string{"low", "high"}) {
			t.Errorf("efforts = %v", e.Efforts)
		}
	}
}

// Bare model ids still resolve to providers; a group id never resolves to
// one provider's model.
func TestResolveGroupIDs(t *testing.T) {
	groupHome(t)
	saveProvider(t, "a", "m")
	saveProvider(t, "b", "vendor/m")

	if _, _, ok := Resolve("group/auto-m"); ok {
		t.Error("Resolve must not answer for a group")
	}
	if p, model, ok := Resolve("a/m"); !ok || p.ID != "a" || model != "m" {
		t.Errorf("Resolve(a/m) = %q %q %v", p.ID, model, ok)
	}
	if e, ok := ResolveGroup("group/auto-m"); !ok || e.ID != "group/auto-m" || len(e.Members) != 2 {
		t.Errorf("ResolveGroup = %+v, %v", e, ok)
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveGroup("group/auto-m"); ok {
		t.Error("hidden group still resolves")
	}
}

func TestNormalizeAffinity(t *testing.T) {
	for in, want := range map[string]string{"session": "session", " Turn ": "turn", "OFF": "off", "": "", "always": ""} {
		if got := NormalizeAffinity(in); got != want {
			t.Errorf("NormalizeAffinity(%q) = %q, want %q", in, got, want)
		}
	}
}
