package catalog

import (
	"reflect"
	"testing"
)

// The APIs a live list declares survive the cache: written by SaveLive, read
// back by Live, with unknown (empty) kept empty.
func TestApisFieldCacheRoundTrip(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	want := []Model{
		{ID: "apisfield-gpt", Name: "GPT", APIs: []string{"responses"}},
		{ID: "apisfield-claude", Name: "Claude", APIs: []string{"chat", "anthropic"}},
		{ID: "apisfield-plain", Name: "Plain"},
	}
	if err := SaveLive("apisfield", "https://example.test", want); err != nil {
		t.Fatal(err)
	}
	got, _, ok := Live("apisfield")
	if !ok || len(got) != len(want) {
		t.Fatalf("Live = %+v, %v", got, ok)
	}
	for i := range want {
		if got[i].ID != want[i].ID || !reflect.DeepEqual(got[i].APIs, want[i].APIs) {
			t.Errorf("model %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A model's APIs are kept when the live list is decorated from models.dev.
func TestApisFieldSurvivesDecorate(t *testing.T) {
	live := []Model{{ID: "apisfield-x", APIs: []string{"chat"}}}
	known := []Model{{ID: "apisfield-x", Name: "X", Released: "2026-01-01"}}
	out := Decorate(live, known)
	if len(out) != 1 || !reflect.DeepEqual(out[0].APIs, []string{"chat"}) || out[0].Name != "X" {
		t.Errorf("Decorate = %+v", out)
	}
}
