package provider

// Black-box checks of acceptance items 1, 3, 4, 8 and 10 as the spec words
// them, through the exported entry points only (Save, SaveGroup, DeleteGroup,
// ShowGroup, FindGroup, SetAffinity, Catalog) and a fake Codex backend.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// accIsolate_f09 points config, cache and HOME at temp dirs and clears the
// live catalog before and after the test.
func accIsolate_f09(t *testing.T) {
	t.Helper()
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// accSave_f09 saves a ready provider with one key, whose live catalog lists
// the given models at its chat endpoint.
func accSave_f09(t *testing.T, id, chat string, models ...catalog.Model) {
	t.Helper()
	if err := Save(Provider{ID: id, Name: strings.ToUpper(id), Key: "k-" + id, Chat: chat}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(id, chat, models); err != nil {
		t.Fatal(err)
	}
}

// accGroupWith_f09 is the catalog's group that holds the member ref
// (provider/model), with its members as FindGroup resolves them.
func accGroupWith_f09(t *testing.T, ref string) (Group, []Member) {
	t.Helper()
	for _, e := range Catalog() {
		if e.Group == "" {
			continue
		}
		g, ms, ok := FindGroup(e.Group)
		if ok && slices.Contains(memberRefs_f09(ms), ref) {
			return g, ms
		}
	}
	t.Fatalf("no group holds %s; catalog: %v", ref, catalogIDs_f09())
	return Group{}, nil
}

// Item 1: with A offering m and B offering vendor/m, the catalog lists each
// provider's models and then group/auto-m; the group is named by A's display
// name and offers only the reasoning levels both members take.
func TestAcceptance01CatalogListsGroupAfterProviders_f09(t *testing.T) {
	groupSetup_f09(t)
	es := Catalog()
	first := -1
	for i, e := range es {
		if e.Group != "" {
			first = i
			break
		}
	}
	if first < 0 {
		t.Fatalf("no routing group in the catalog: %v", catalogIDs_f09())
	}
	for i, e := range es {
		if (e.Group != "") != (i >= first) {
			t.Fatalf("entry %d (%s) is out of place: groups must come after every provider's models: %v", i, e.ID, catalogIDs_f09())
		}
	}
	g := es[first]
	if g.ID != "group/auto-m" || g.Group != "auto-m" {
		t.Fatalf("group entry: id %q group %q", g.ID, g.Group)
	}
	if g.Name != "M" {
		t.Errorf("group name %q, want A's display name M", g.Name)
	}
	if !slices.Equal(g.Efforts, []string{"high"}) {
		t.Errorf("group efforts %v, want only the level both members take: [high]", g.Efforts)
	}
	ids := catalogIDs_f09()
	for _, want := range []string{"a/m", "b/vendor/m"} {
		if !slices.Contains(ids, want) {
			t.Errorf("catalog lacks %s: %v", want, ids)
		}
	}
}

// Item 3: deleting the automatic group makes it unavailable; showing it again
// brings it back.
func TestAcceptance03DeleteAndShowAutoGroup_f09(t *testing.T) {
	groupSetup_f09(t)
	if _, ms, ok := FindGroup("group/auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("auto-m before delete: found %v, members %v", ok, memberRefs_f09(ms))
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("group/auto-m"); ok && len(ms) > 0 {
		t.Fatalf("auto-m after delete still resolves to %v", memberRefs_f09(ms))
	}
	if slices.Contains(catalogIDs_f09(), "group/auto-m") {
		t.Fatal("deleted auto-m still listed in the catalog")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, ms, ok := FindGroup("group/auto-m"); !ok || len(ms) != 2 {
		t.Fatalf("auto-m after show: found %v, members %v", ok, memberRefs_f09(ms))
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Error("deleting a group that does not exist must fail")
	}
}

// Item 4: an empty group is refused with the spec's message, a provider may
// not be called group, and a group nested in a group is ignored.
func TestAcceptance04GroupRules_f09(t *testing.T) {
	groupSetup_f09(t)
	err := SaveGroup(Group{ID: "empty", Members: nil})
	if err == nil || err.Error() != "组里至少要有一个模型" {
		t.Fatalf("empty group: error %v, want 组里至少要有一个模型", err)
	}
	if err := Save(Provider{ID: "group", Name: "G", Key: "k", Chat: "https://g.example/v1"}); err == nil {
		t.Error("a provider with id group must be refused")
	}
	if err := SaveGroup(Group{ID: "nest", Members: []string{"group/auto-m", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("nest")
	if !ok || !slices.Equal(g.Members, []string{"a/only-a"}) || !slices.Equal(memberRefs_f09(ms), []string{"a/only-a"}) {
		t.Fatalf("nested group member not ignored: found %v, members %v, resolved %v", ok, g.Members, memberRefs_f09(ms))
	}
	if err := SaveGroup(Group{ID: "only-nest", Members: []string{"group/auto-m"}}); err == nil || err.Error() != "组里至少要有一个模型" {
		t.Errorf("a group whose only member is a group: error %v", err)
	}
	// id defaults from the name; name defaults from the id; bad values fall back
	if err := SaveGroup(Group{Name: "Mine Set", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("mine-set"); !ok || g.Name != "Mine Set" {
		t.Errorf("id from name: found %v, group %+v", ok, g)
	}
	if err := SaveGroup(Group{ID: "odd", Members: []string{"a/m"}, Routing: "random", Affinity: "sometimes"}); err != nil {
		t.Fatal(err)
	}
	if g, _, ok := FindGroup("odd"); !ok || g.Routing != "" || g.Affinity != "" || g.Name != "odd" {
		t.Errorf("invalid routing/affinity should fall back: found %v, group %+v", ok, g)
	}
	if err := SetAffinity("a", "sometimes"); err == nil {
		t.Error("an unknown affinity must be refused")
	}
	if err := SetAffinity("a", "turn"); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("a"); err != nil || p.Affinity != "turn" {
		t.Errorf("provider affinity after SetAffinity: %v, %+v", err, p)
	}
}

// Item 8: the same model written several ways is one automatic group named
// with a display name; the :batch variant and the dotted v1.beta are not
// rewritten into one another.
func TestAcceptance08SameModelOneAutoGroup_f09(t *testing.T) {
	accIsolate_f09(t)
	accSave_f09(t, "p1", "https://p1.example/v1", catalog.Model{ID: "claude-opus-5-5"})
	accSave_f09(t, "p2", "https://p2.example/v1", catalog.Model{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"})
	accSave_f09(t, "p3", "https://p3.example/v1", catalog.Model{ID: "Claude-Opus-5.5"})
	accSave_f09(t, "p4", "https://p4.example/v1", catalog.Model{ID: "claude-opus-5-5-20260801"})
	accSave_f09(t, "q1", "https://q1.example/v1", catalog.Model{ID: "claude-opus-5-5:batch"}, catalog.Model{ID: "v1.beta"})
	accSave_f09(t, "q2", "https://q2.example/v1", catalog.Model{ID: "claude-opus-5-5:batch"}, catalog.Model{ID: "v1.beta"})
	accSave_f09(t, "q3", "https://q3.example/v1", catalog.Model{ID: "v1-beta"})

	g, ms := accGroupWith_f09(t, "p1/claude-opus-5-5")
	if g.ID != "auto-claude-opus-5-5" || g.Name != "Claude Opus 5.5" || !g.Auto {
		t.Fatalf("claude group: %+v", g)
	}
	want := []string{"p1/claude-opus-5-5", "p2/anthropic/claude-opus-5.5", "p3/Claude-Opus-5.5", "p4/claude-opus-5-5-20260801"}
	if got := memberRefs_f09(ms); !slices.Equal(got, want) {
		t.Errorf("claude members %v, want %v", got, want)
	}

	_, mb := accGroupWith_f09(t, "q1/claude-opus-5-5:batch")
	if got, want := memberRefs_f09(mb), []string{"q1/claude-opus-5-5:batch", "q2/claude-opus-5-5:batch"}; !slices.Equal(got, want) {
		t.Errorf(":batch members %v, want %v (must not join the base model)", got, want)
	}

	_, mv := accGroupWith_f09(t, "q1/v1.beta")
	if got, want := memberRefs_f09(mv), []string{"q1/v1.beta", "q2/v1.beta"}; !slices.Equal(got, want) {
		t.Errorf("v1.beta members %v, want %v (v1-beta must not join)", got, want)
	}
}

// Item 10: the models list request carries the newest of the Codex CLI's
// version and the cache's; without the CLI, the cache's.
func TestAcceptance10CodexModelsAskNewestVersion_f09(t *testing.T) {
	var got string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("client_version")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models": [{"slug": "gpt-6-sol", "display_name": "GPT-6 Sol", "visibility": "list", "priority": 1,
			"supported_reasoning_levels": [{"effort": "low"}]}]}`))
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	sign := func(context.Context, *http.Request, []byte) error { return nil }

	withCodexVersions_f09(t, "codex-cli 0.155.1", "0.154.0")
	if _, err := codexModels(context.Background(), sign); err != nil {
		t.Fatal(err)
	}
	if got != "0.155.1" {
		t.Errorf("CLI 0.155.1 and cache 0.154.0: request carried client_version %q", got)
	}

	withCodexVersions_f09(t, "", "0.154.0")
	got = ""
	if _, err := codexModels(context.Background(), sign); err != nil {
		t.Fatal(err)
	}
	if got != "0.154.0" {
		t.Errorf("no CLI, cache 0.154.0: request carried client_version %q", got)
	}
}
