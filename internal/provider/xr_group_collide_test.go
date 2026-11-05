package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// Two model names that slug alike ("x:batch" and "x-batch") both form an
// automatic group, under distinct ids, rather than one being dropped (I7).
func TestXRAutoGroupsSlugCollisionKeepsBoth(t *testing.T) {
	xrGroupEnvCollide(t)
	if err := Save(Provider{ID: "a", Name: "A", Key: "ka", Chat: "https://a.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Key: "kb", Chat: "https://b.example/v1"}); err != nil {
		t.Fatal(err)
	}
	both := []catalog.Model{{ID: "gpt-x:batch", Name: "GPT X Batch"}, {ID: "gpt-x-batch", Name: "GPT X Dash"}}
	if err := catalog.SaveLive("a", "https://a.example/v1", both); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("b", "https://b.example/v1", both); err != nil {
		t.Fatal(err)
	}

	gs := autoGroups()
	if len(gs) != 2 {
		t.Fatalf("auto groups = %+v, want two", gs)
	}
	if gs[0].ID == gs[1].ID {
		t.Fatalf("both groups share id %q", gs[0].ID)
	}
	for _, g := range gs {
		if len(g.Members) != 2 {
			t.Fatalf("group %s members %v, want both vendors", g.ID, g.Members)
		}
		if _, ms, ok := FindGroup(g.ID); !ok || len(ms) != 2 {
			t.Fatalf("FindGroup(%s) = %d members, ok=%v", g.ID, len(ms), ok)
		}
	}
}

func xrGroupEnvCollide(t *testing.T) {
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
