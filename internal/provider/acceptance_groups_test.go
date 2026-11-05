package provider

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// accIsolate points config, cache and home at fresh directories.
func accIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// accTwo saves provider a (model m) and provider b (model vendor/m).
func accTwo(t *testing.T) {
	t.Helper()
	accIsolate(t)
	if err := Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "http://a.test/v1", Models: []string{"m", "only-a"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Key: "kb", Chat: "http://b.test/v1", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
}

func accIDs() []string {
	var out []string
	for _, e := range Catalog() {
		out = append(out, e.ID)
	}
	return out
}

// (a)(b) Catalog lists each provider's models, then the auto group, after them.
func TestAccCatalogAutoGroup(t *testing.T) {
	accTwo(t)
	ids := accIDs()
	ia, ib, ig := slices.Index(ids, "a/m"), slices.Index(ids, "b/vendor/m"), slices.Index(ids, "group/auto-m")
	if ia < 0 || ib < 0 || ig < 0 {
		t.Fatalf("catalog %v lacks a/m, b/vendor/m or group/auto-m", ids)
	}
	if ig < ia || ig < ib {
		t.Fatalf("group must come after the providers' models: %v", ids)
	}
	for _, e := range Catalog() {
		if e.ID == "group/auto-m" && (e.Group != "auto-m" || e.Provider.ID != groupPrefix) {
			t.Fatalf("group entry %+v", e)
		}
	}
}

// (a) the exported group API: Group fields, Member fields, FindGroup ordering.
func TestAccSaveFindGroupOrder(t *testing.T) {
	accTwo(t)
	// an uppercase id is normalized to lowercase; a repeated member counts once
	if err := SaveGroup(Group{ID: "Mine", Name: "Mine", Members: []string{"b/vendor/m", "a/only-a", "b/vendor/m"}, Routing: "order"}); err != nil {
		t.Fatalf("SaveGroup: %v", err)
	}
	g, ms, ok := FindGroup("group/mine")
	if !ok {
		t.Fatal("group/mine not found")
	}
	if g.ID != "mine" || g.Name != "Mine" || g.Routing != Ordered {
		t.Fatalf("group %+v", g)
	}
	if !slices.Equal(g.Members, []string{"b/vendor/m", "a/only-a"}) {
		t.Fatalf("members %v", g.Members)
	}
	if len(ms) != 2 {
		t.Fatalf("ready members %+v", ms)
	}
	if ms[0].Provider.ID != "b" || ms[0].Model != "vendor/m" || ms[0].Ref != "b/vendor/m" {
		t.Errorf("member 0 %+v", ms[0])
	}
	if ms[1].Provider.ID != "a" || ms[1].Model != "only-a" {
		t.Errorf("member 1 %+v", ms[1])
	}
}

// (d) deleting an auto group hides it; showing it brings it back.
func TestAccDeleteShowAutoGroup(t *testing.T) {
	accTwo(t)
	if _, _, ok := FindGroup("group/auto-m"); !ok {
		t.Fatal("auto-m not found before delete")
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); ok {
		t.Fatal("auto-m still found after delete")
	}
	if slices.Contains(accIDs(), "group/auto-m") {
		t.Fatal("auto-m still in catalog after delete")
	}
	if err := ShowGroup("group/auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("group/auto-m"); !ok {
		t.Fatal("auto-m not back after show")
	}
	if err := DeleteGroup("no-such-group"); err == nil {
		t.Error("deleting an unknown group must fail")
	}
}

// (e) empty members fail, provider id "group" fails, a group in a group is ignored.
func TestAccSaveGroupRules(t *testing.T) {
	accTwo(t)
	if err := SaveGroup(Group{ID: "empty", Members: []string{" "}}); err == nil {
		t.Error("empty members saved")
	}
	if err := SaveGroup(Group{ID: "none"}); err == nil {
		t.Error("no members saved")
	}
	if err := Save(Provider{ID: "group", Name: "G", Key: "k", Chat: "http://g.test/v1", Models: []string{"m"}}); err == nil {
		t.Error(`provider id "group" saved`)
	}
	if err := SaveGroup(Group{ID: "nest", Members: []string{"group/auto-m", "a/only-a"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("nest")
	if !ok || !slices.Equal(g.Members, []string{"a/only-a"}) || len(ms) != 1 {
		t.Fatalf("nest: %+v %+v %v", g, ms, ok)
	}
}

// Normalization: unknown routing and affinity fall back, known ones stick.
func TestAccNormalizeRoutingAffinity(t *testing.T) {
	accTwo(t)
	if err := SaveGroup(Group{ID: "r", Members: []string{"a/m"}, Routing: "ordered", Affinity: "bogus"}); err != nil {
		t.Fatal(err)
	}
	g, _, _ := FindGroup("r")
	if g.Routing != "" || g.Affinity != "" {
		t.Errorf("bad values kept: %+v", g)
	}
	if err := SaveGroup(Group{ID: "r", Members: []string{"a/m"}, Routing: "least-used", Affinity: "session"}); err != nil {
		t.Fatal(err)
	}
	g, _, _ = FindGroup("r")
	if g.Routing != LeastUsed || g.Affinity != "session" {
		t.Errorf("good values lost: %+v", g)
	}
	if err := SetAffinity("a", "turn"); err != nil {
		t.Fatal(err)
	}
	p, err := Find("a")
	if err != nil || p.Affinity != "turn" {
		t.Fatalf("provider affinity %v %+v", err, p)
	}
	if err := SetAffinity("a", "nonsense"); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("a"); p.Affinity != "" {
		t.Errorf("bad affinity kept: %q", p.Affinity)
	}
}

// (a) SaveGroup on a discovered group makes it the user's; DeleteGroup then removes it.
func TestAccSaveMakesAutoUserOwned(t *testing.T) {
	accTwo(t)
	if err := SaveGroup(Group{ID: "auto-m", Name: "Mine now", Members: []string{"a/m"}}); err != nil {
		t.Fatal(err)
	}
	g, _, ok := FindGroup("auto-m")
	if !ok || g.Auto || g.Name != "Mine now" {
		t.Fatalf("after save: %+v %v", g, ok)
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := FindGroup("auto-m"); ok {
		t.Error("user group still there after delete")
	}
}

// (8) model names that differ only in case, vendor prefix, dotted version
// or date are one model; colon variants and v1.beta are left alone.
func TestAccCanonicalModel(t *testing.T) {
	for _, s := range []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801", "claude-opus-5.5"} {
		if got := CanonicalModel(s); got != "claude-opus-5-5" {
			t.Errorf("CanonicalModel(%q) = %q", s, got)
		}
	}
	if got := CanonicalModel("foo:batch"); got != "foo:batch" {
		t.Errorf("colon variant rewritten: %q", got)
	}
	if got := CanonicalModel("claude-opus-5.5:batch"); got == "claude-opus-5-5" {
		t.Errorf("colon variant merged with the base model: %q", got)
	}
	if got := CanonicalModel("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta rewritten: %q", got)
	}
}

// (8) the auto group for the spelling variants is one group.
func TestAccAutoGroupAcrossSpellings(t *testing.T) {
	accIsolate(t)
	if err := Save(Provider{ID: "x", Name: "X", Key: "kx", Chat: "http://x.test/v1", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "y", Name: "Y", Key: "ky", Chat: "http://y.test/v1", Models: []string{"anthropic/claude-opus-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "z", Name: "Z", Key: "kz", Chat: "http://z.test/v1", Models: []string{"Claude-Opus-5.5"}}); err != nil {
		t.Fatal(err)
	}
	g, ms, ok := FindGroup("group/auto-claude-opus-5-5")
	if !ok || len(ms) != 3 {
		t.Fatalf("auto group %+v members %+v ok=%v", g, ms, ok)
	}
	if !g.Auto {
		t.Error("discovered group not marked auto")
	}
}

// (10) the Codex client version is the newest of the CLI, the cache and the default.
func TestAccCodexVersionNewest(t *testing.T) {
	accIsolate(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.154.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reset := func() {
		codexVersionMu.Lock()
		codexVersion, codexVersionAt = "", time.Time{}
		codexVersionMu.Unlock()
	}
	oldCLI := codexVersionFromCLI
	t.Cleanup(func() { codexVersionFromCLI = oldCLI; reset() })

	reset()
	codexVersionFromCLI = func() (string, error) { return "0.155.1", nil }
	if v := codexClientVersionNow(); v != "0.155.1" {
		t.Errorf("with CLI 0.155.1: got %q", v)
	}
	reset()
	codexVersionFromCLI = func() (string, error) { return "", os.ErrNotExist }
	if v := codexClientVersionNow(); v != "0.154.0" {
		t.Errorf("without CLI: got %q", v)
	}
	if !strings.HasPrefix(codexClientVersionNow(), "0.154") {
		t.Error("version not kept")
	}
}
