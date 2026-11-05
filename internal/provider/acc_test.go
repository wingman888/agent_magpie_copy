package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Acceptance A1: two providers serve "m" under their own names. The catalog
// lists each model, and the auto group after the providers.
func TestRgAccA1AutoGroupInCatalog(t *testing.T) {
	rgpGroupsEnv(t)
	rgpAddProvider(t, "a", catalog.Model{ID: "m", Name: "M"})
	rgpAddProvider(t, "b", catalog.Model{ID: "vendor/m", Name: "Vendor M"})
	got := rgpIds(Catalog())
	a, b, g := slices.Index(got, "a/m"), slices.Index(got, "b/vendor/m"), slices.Index(got, "group/auto-m")
	if a < 0 || b < 0 || g < 0 {
		t.Fatalf("catalog %v: want a/m, b/vendor/m and group/auto-m", got)
	}
	if !(a < g && b < g) {
		t.Fatalf("catalog %v: the routing group must come after the providers' models", got)
	}
	_, ms, ok := FindGroup("auto-m")
	if !ok || len(ms) != 2 || ms[0].Provider.ID != "a" || ms[1].Provider.ID != "b" {
		t.Fatalf("auto-m members: ok=%v %+v", ok, ms)
	}
}

// Acceptance A3: deleting an auto group hides it; showing it brings it back.
// A group the user saves over an auto one is theirs, and deleting it leaves
// the auto group hidden.
func TestRgAccA3DeleteAndShowAutoGroup(t *testing.T) {
	rgpGroupsEnv(t)
	rgpAddProvider(t, "a", catalog.Model{ID: "m", Name: "M"})
	rgpAddProvider(t, "b", catalog.Model{ID: "vendor/m", Name: "Vendor M"})
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("no auto-m group to start with")
	}
	if err := DeleteGroup("group/auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("auto-m still found after delete")
	}
	if slices.Contains(rgpIds(Catalog()), "group/auto-m") {
		t.Fatal("auto-m still in the catalog after delete")
	}
	if err := ShowGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); !ok {
		t.Fatal("auto-m not back after ShowGroup")
	}

	// saving over it makes it the user's
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("auto-m")
	if !ok || g.Auto || len(ms) != 1 {
		t.Fatalf("saved over auto-m: ok=%v auto=%v members=%d", ok, g.Auto, len(ms))
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Fatal("auto-m still found after deleting the user's copy; magpie would find it again")
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Error("deleting a group that does not exist must fail")
	}
}

// Acceptance A4: validation of groups and provider ids.
func TestRgAccA4GroupValidation(t *testing.T) {
	rgpGroupsEnv(t)
	rgpAddProvider(t, "a", catalog.Model{ID: "only-a", Name: "Only A"})
	if err := SaveGroup(Group{ID: "empty"}); err == nil {
		t.Error("a group with no members was saved")
	}
	if err := SaveGroup(Group{ID: "nested", Members: []string{"group/other"}}); err == nil {
		t.Error("a group of only groups was saved")
	}
	if err := SaveGroup(Group{ID: "nest", Members: []string{"group/mine", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("nest")
	if !ok || !slices.Equal(g.Members, []string{"a/only-a"}) || len(ms) != 1 {
		t.Fatalf("nested member not ignored: ok=%v members=%v resolved=%d", ok, g.Members, len(ms))
	}
	if err := Save(Provider{ID: "group", Name: "Group", Chat: "https://g.example/v1", Key: "sk-g"}); err == nil {
		t.Error(`a provider with id "group" was saved`)
	}
}

// Acceptance A8: spellings of one model make one auto group, named by its
// display name; the :batch variant and dotted names other than versions
// stay as they are.
func TestRgAccA8SpellingsMakeOneAutoGroup(t *testing.T) {
	rgpGroupsEnv(t)
	rgpAddProvider(t, "p1", catalog.Model{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"})
	rgpAddProvider(t, "p2", catalog.Model{ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"})
	rgpAddProvider(t, "p3", catalog.Model{ID: "Claude-Opus-5.5", Name: "Claude Opus 5.5"})
	rgpAddProvider(t, "p4", catalog.Model{ID: "claude-opus-5-5-20260801", Name: "Claude Opus 5.5"})
	rgpAddProvider(t, "p5", catalog.Model{ID: "claude-opus-5-5:batch", Name: "Opus batch"})
	rgpAddProvider(t, "p6", catalog.Model{ID: "claude-opus-5-5:batch", Name: "Opus batch"})
	g, ms, ok := FindGroup("auto-claude-opus-5-5")
	if !ok {
		t.Fatal("no auto group for the four spellings")
	}
	if g.Name != "Claude Opus 5.5" {
		t.Errorf("name %q, want the display name", g.Name)
	}
	if len(ms) != 4 {
		t.Errorf("members %d, want the four spellings: %+v", len(ms), ms)
	}
	for _, id := range []string{"p5", "p6"} {
		for _, m := range ms {
			if m.Provider.ID == id {
				t.Errorf("batch variant %s joined the Opus group", id)
			}
		}
	}
	bg, bms, ok := FindGroup("auto-claude-opus-5-5-batch")
	if !ok || len(bms) != 2 {
		t.Fatalf("batch variant group: ok=%v members=%d (%s)", ok, len(bms), bg.ID)
	}
	if modelKey("v1.beta") != "v1.beta" || modelKey("claude-opus-5.5:batch") == modelKey("claude-opus-5.5") {
		t.Error("modelKey rewrote a non-version dot or merged a variant")
	}
}

// Acceptance A10: the Codex models list asks with the newest of the installed
// CLI's version, the cache's and the built-in default.
func TestRgAccA10CodexClientVersion(t *testing.T) {
	home := rgpGroupsEnv(t)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.154.0","models":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	oldCmd := codexVersionCommand
	t.Cleanup(func() {
		codexVersionCommand = oldCmd
		rgpAccResetCodexVersion()
	})

	codexVersionCommand = func() (string, error) { return "codex-cli 0.155.1\n", nil }
	rgpAccResetCodexVersion()
	if v := codexVersionNow(); v != "0.155.1" {
		t.Errorf("with a CLI reporting 0.155.1 and a cache of 0.154.0: %s, want 0.155.1", v)
	}

	codexVersionCommand = func() (string, error) { return "", errors.New("codex: not found") }
	rgpAccResetCodexVersion()
	if v := codexVersionNow(); v != "0.154.0" {
		t.Errorf("without a CLI: %s, want 0.154.0", v)
	}

	// the models list itself asks with that version
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("client_version")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	oldBase := CodexBase
	t.Cleanup(func() { CodexBase = oldBase })
	CodexBase = srv.URL
	codexVersionCommand = func() (string, error) { return "codex-cli 0.155.1\n", nil }
	rgpAccResetCodexVersion()
	_, _ = codexModels(context.Background(), func(context.Context, *http.Request, []byte) error { return nil })
	if asked != "0.155.1" {
		t.Errorf("models list asked with client_version %q, want 0.155.1", asked)
	}
}

func rgpAccResetCodexVersion() {
	codexVersion.Lock()
	codexVersion.v = ""
	codexVersion.Unlock()
}

// Acceptance: routing values are order, rotate, least-used, or "" for smart;
// anything else falls back to "" on groups and providers alike; the affinity
// values are session, turn, off, or "".
func TestRgAccRoutingAndAffinityValues(t *testing.T) {
	rgpGroupsEnv(t)
	rgpAddProvider(t, "a", catalog.Model{ID: "m", Name: "M"})
	for _, r := range []string{"order", "rotate", "least-used"} {
		if err := SaveGroup(Group{ID: "g-" + r, Members: []string{"a/m"}, Routing: r}); err != nil {
			t.Fatal(err)
		}
		if g, _, ok := FindGroup("g-" + r); !ok || g.Routing != r {
			t.Errorf("group routing %q became %q (found=%v)", r, g.Routing, ok)
		}
	}
	if err := SaveGroup(Group{ID: "bad", Members: []string{"a/m"}, Routing: "ordered", Affinity: "always"}); err != nil {
		t.Fatal(err)
	}
	if g, _, _ := FindGroup("bad"); g.Routing != "" || g.Affinity != "" {
		t.Errorf("invalid group values kept: routing %q affinity %q", g.Routing, g.Affinity)
	}
	if err := Save(Provider{ID: "p", Name: "P", Chat: "https://p.example/v1", Key: "sk-p", Routing: "ordered", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	if p, err := Find("p"); err != nil || p.Routing != "" || p.Affinity != "" {
		t.Errorf("invalid provider values kept: %+v %v", p, err)
	}
	if err := SetAffinity("p", "turn"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("p"); p.Affinity != "turn" {
		t.Errorf("SetAffinity turn became %q", p.Affinity)
	}
}
