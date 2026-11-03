package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCatalog puts a valid models.dev-shaped file where load() will find it;
// load() falls back to the machine's own caches, so a malformed file would
// silently read the developer's real catalog.
func writeCatalog(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	Reset()
	t.Cleanup(Reset)
}

func TestProviderReadsTemperatureCapability(t *testing.T) {
	writeCatalog(t, `{
	  "openai": {
	    "models": {
	      "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","temperature":false,
	                  "reasoning_options":[{"type":"effort","values":["low","high"]}]},
	      "gpt-4.1": {"id":"gpt-4.1","name":"GPT-4.1","temperature":true}
	    }
	  }
	}`)

	ms := Provider("openai")
	if len(ms) != 2 {
		t.Fatalf("models: %+v", ms)
	}
	byID := map[string]Model{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	if p := byID["gpt-5.5"].Temperature; p == nil || *p {
		t.Fatalf("gpt-5.5 temperature: %v", p)
	}
	if p := byID["gpt-4.1"].Temperature; p == nil || !*p {
		t.Fatalf("gpt-4.1 temperature: %v", p)
	}
	if got := byID["gpt-5.5"].Efforts; len(got) != 2 || got[0] != "low" {
		t.Fatalf("efforts: %v", got)
	}
}

func TestDecorateCarriesTemperature(t *testing.T) {
	no, yes := false, true
	live := []Model{{ID: "new-model"}, {ID: "gpt-5.5"}}
	known := []Model{
		{ID: "gpt-5.5", Name: "GPT-5.5", Temperature: &no, Released: "2026-04-23"},
		{ID: "old", Temperature: &yes},
	}
	out := Decorate(live, known)
	if out[0].Temperature != nil {
		t.Fatalf("an unknown model gained a capability: %v", *out[0].Temperature)
	}
	if out[1].Temperature == nil || *out[1].Temperature || out[1].Name != "GPT-5.5" {
		t.Fatalf("decorated: %+v", out[1])
	}
}
