package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The live cache keeps each model's declared APIs, and a cache written
// before the field existed reads back with none (unknown).
func TestLiveKeepsAPIs(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	want := []Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic"}},
		{ID: "plain"},
	}
	if err := SaveLive("copilot", "https://example.test", want); err != nil {
		t.Fatal(err)
	}
	got, _, ok := Live("copilot")
	if !ok || len(got) != 3 {
		t.Fatalf("live: ok=%v %+v", ok, got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i].APIs, want[i].APIs) {
			t.Fatalf("model %s APIs = %v, want %v", got[i].ID, got[i].APIs, want[i].APIs)
		}
	}
}

func TestLiveWithoutAPIsIsUnknown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if err := os.MkdirAll(filepath.Dir(LivePath("old")), 0o755); err != nil {
		t.Fatal(err)
	}
	old := `{"fetched":"2026-01-01T00:00:00Z","base":"https://x","models":[{"ID":"gpt-5.5","Name":"GPT-5.5"}]}`
	if err := os.WriteFile(LivePath("old"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, ok := Live("old")
	if !ok || len(got) != 1 || got[0].APIs != nil {
		t.Fatalf("old cache: ok=%v %+v", ok, got)
	}
}
