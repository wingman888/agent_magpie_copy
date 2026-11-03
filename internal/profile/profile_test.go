package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Applying a profile must set Claude Code's main model before its tiers: the
// tiers are only allowed once the main model routes through magpie. The keys
// are chosen so that alphabetical order would put the tier first.
func TestApplyRoutesClaudeMainModelBeforeTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{
		ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k",
		Models: []string{"deepseek-pro", "deepseek-flash"},
	}); err != nil {
		t.Fatal(err)
	}

	p := Profile{
		"claude.haiku": "deepseek/deepseek-flash",
		"claude.model": "deepseek/deepseek-pro",
	}
	if _, err := Apply(p); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	path := filepath.Join(home, ".claude", "settings.json")
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_MODEL"); v != "deepseek/deepseek-pro" {
		t.Fatalf("main model = %q", v)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/deepseek-flash" {
		t.Fatalf("haiku = %q", v)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_OPUS_MODEL"); v != "deepseek/deepseek-pro" {
		t.Fatalf("opus = %q, want it to follow the main model", v)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
