package provider

import (
	"reflect"
	"testing"
)

// fbcfgIsolate points the provider file at a temp dir so tests never touch
// the user's real config.
func fbcfgIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestFbcfgFallbackSaveNormalizes(t *testing.T) {
	fbcfgIsolate(t)
	if err := Save(Provider{ID: "fbcfg-a", Name: "fbcfg A", Chat: "https://a.example/v1", Key: "sk-fbcfg-1234567890",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbcfg-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("Fallback = %q, want %q", p.Fallback, want)
	}

	// saving again without a fallback clears it (nil, so the JSON key is omitted)
	p.Fallback = nil
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("fbcfg-a")
	if len(p.Fallback) != 0 {
		t.Fatalf("Fallback after clear = %q, want empty", p.Fallback)
	}
}

func TestFbcfgFallbackSurvivesReload(t *testing.T) {
	fbcfgIsolate(t)
	if err := Save(Provider{ID: "fbcfg-r", Name: "fbcfg R", Chat: "https://r.example/v1", Key: "sk-fbcfg-abcdefgh",
		Fallback: []string{"fbcfg-s/m1"}}); err != nil {
		t.Fatal(err)
	}
	// All() re-reads the file from disk, which is what a restart does
	for _, p := range All() {
		if p.ID == "fbcfg-r" {
			if !reflect.DeepEqual(p.Fallback, []string{"fbcfg-s/m1"}) {
				t.Fatalf("Fallback after reload = %q", p.Fallback)
			}
			return
		}
	}
	t.Fatal("provider fbcfg-r not listed")
}
