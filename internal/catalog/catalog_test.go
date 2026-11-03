package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// catIsolateCodex points HOME at a fresh tree and returns the Codex cache path.
func catIsolateCodex(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return filepath.Join(home, ".codex", "models_cache.json")
}

func catIDsOf(ms []Model) map[string]int {
	out := map[string]int{}
	for _, m := range ms {
		out[m.ID]++
	}
	return out
}

// Without Codex's cache, the built-in list is the answer, gpt-5.5 included.
func TestCatCodexWithoutCacheIsBuiltin(t *testing.T) {
	catIsolateCodex(t)
	if got := catIDsOf(Codex()); got["gpt-5.5"] != 1 {
		t.Fatalf("gpt-5.5 missing without a cache: %v", got)
	}
}

// With a cache, the cache's models come first in its order, the built-in
// models it does not name follow, and nothing is listed twice.
func TestCatCodexUnionsCacheWithBuiltin(t *testing.T) {
	path := catIsolateCodex(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	cache := `{"models":[
		{"slug":"gpt-7-new","display_name":"New","priority":1},
		{"slug":"gpt-5.5","display_name":"GPT-5.5 from cache","priority":2},
		{"slug":"hidden-one","display_name":"Hidden","visibility":"hide","priority":0}
	]}`
	if err := os.WriteFile(path, []byte(cache), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Codex()
	if len(got) < 2 || got[0].ID != "gpt-7-new" || got[1].ID != "gpt-5.5" {
		t.Fatalf("cache order not kept first: %+v", got)
	}
	if got[1].Name != "GPT-5.5 from cache" {
		t.Errorf("cache entry should win over built-in: %+v", got[1])
	}
	counts := catIDsOf(got)
	for id, n := range counts {
		if n != 1 {
			t.Errorf("%s listed %d times", id, n)
		}
	}
	if counts["gpt-5.5"] != 1 {
		t.Errorf("gpt-5.5 missing: %v", counts)
	}
	if counts["gpt-6-astra"] != 1 {
		t.Errorf("built-in model absent from the cache not added: %v", counts)
	}
	if counts["hidden-one"] != 0 {
		t.Errorf("hidden cache model listed: %v", counts)
	}
}
