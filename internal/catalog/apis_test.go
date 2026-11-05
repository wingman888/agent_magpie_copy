package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLiveRoundTripsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := SaveLive("apis-rt", "https://x", []Model{
		{ID: "gpt-6-sol", APIs: []string{"responses"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	ms, _, ok := Live("apis-rt")
	if !ok || len(ms) != 2 {
		t.Fatalf("live: %+v ok=%v", ms, ok)
	}
	if !slices.Equal(ms[0].APIs, []string{"responses"}) {
		t.Fatalf("APIs lost on round trip: %v", ms[0].APIs)
	}
	if ms[1].APIs != nil {
		t.Fatalf("a model without APIs must read back unknown: %v", ms[1].APIs)
	}
}

func TestLiveWithoutAPIsStillReads(t *testing.T) {
	// a cache written before the APIs field existed
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := LivePath("apis-old")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"fetched":"2026-01-01T00:00:00Z","base":"https://x","models":[{"ID":"m1","Name":"M1"}]}`
	if err := os.WriteFile(p, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	ms, _, ok := Live("apis-old")
	if !ok || len(ms) != 1 || ms[0].ID != "m1" || ms[0].APIs != nil {
		t.Fatalf("old cache: %+v ok=%v", ms, ok)
	}
}

func TestModelAPIsOmittedWhenUnknown(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := SaveLive("apis-omit", "https://x", []Model{{ID: "m"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(LivePath("apis-omit"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "APIs") {
		t.Fatalf("an unknown API list must not be written: %s", b)
	}
}
