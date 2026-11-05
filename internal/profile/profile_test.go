package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// A profile that names a Claude Code tier and its main model must apply the
// model first, or the tier would be refused for lack of a magpie model.
func TestApplyModelBeforeTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p := Profile{
		"claude.haiku": "deepseek/flash",
		"claude.model": "deepseek/pro",
	}
	if _, err := Apply(p); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/flash" {
		t.Errorf("haiku = %q", v)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_OPUS_MODEL"); v != "deepseek/pro" {
		t.Errorf("opus = %q, want the profile's main model", v)
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Errorf("theme lost: %q", v)
	}
}
