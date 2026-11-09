package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestF05FallbackNormalizeOnSave(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	err := Save(Provider{
		ID:       "f05a",
		Name:     "F05A",
		Chat:     "https://example.com/v1",
		Key:      "k",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"},
	})
	if err != nil {
		t.Fatal(err)
	}

	p, err := Find("f05a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("Fallback = %q, want %q", p.Fallback, want)
	}

	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"fallback"`) {
		t.Fatalf("providers.json has no fallback key:\n%s", raw)
	}
}

func TestF05FallbackOmittedWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := Save(Provider{ID: "f05b", Name: "F05B", Chat: "https://example.com/v1", Key: "k", Fallback: []string{" ", ""}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("f05b")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fallback) != 0 {
		t.Fatalf("Fallback = %q, want empty", p.Fallback)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fallback") {
		t.Fatalf("empty fallback should be omitted from providers.json:\n%s", raw)
	}
}
