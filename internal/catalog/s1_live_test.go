package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// s1Cache points the live-list cache at a fresh directory for one test.
func s1Cache(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestS1LiveKeepsAPIs(t *testing.T) {
	s1Cache(t)
	in := []Model{
		{ID: "gpt-x", Name: "GPT X", APIs: []string{"responses", "chat"}},
		{ID: "plain", Name: "Plain"},
	}
	if err := SaveLive("s1vendor", "https://x.test", in); err != nil {
		t.Fatal(err)
	}
	out, _, ok := Live("s1vendor")
	if !ok || len(out) != 2 {
		t.Fatalf("live: %+v ok=%v", out, ok)
	}
	if !slices.Equal(out[0].APIs, []string{"responses", "chat"}) {
		t.Errorf("APIs round trip: %v", out[0].APIs)
	}
	if out[1].APIs != nil {
		t.Errorf("unknown APIs should stay nil, got %v", out[1].APIs)
	}
}

func TestS1LiveOldCacheWithoutAPIs(t *testing.T) {
	s1Cache(t)
	p := LivePath("s1old")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"fetched":"2026-01-01T00:00:00Z","base":"https://x.test","models":[{"ID":"gpt-x","Name":"GPT X"}]}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, ok := Live("s1old")
	if !ok || len(out) != 1 || out[0].ID != "gpt-x" || out[0].APIs != nil {
		t.Fatalf("old cache read: %+v ok=%v", out, ok)
	}
}
