package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Applying a profile that names no Claude Code tier returns the tiers that
// were pinned before to following the main model.
func TestApplyWithoutTiersClearsPins(t *testing.T) {
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
	withTier := Profile{"claude.model": "deepseek/pro", "claude.haiku": "deepseek/flash"}
	if _, err := Apply(withTier); err != nil {
		t.Fatalf("apply with tier: %v", err)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/flash" {
		t.Fatalf("setup: haiku = %q", v)
	}

	if _, err := Apply(Profile{"claude.model": "deepseek/pro"}); err != nil {
		t.Fatalf("apply without tier: %v", err)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/pro" {
		t.Errorf("haiku = %q, want it to follow the main model again", v)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_SMALL_FAST_MODEL"); v != "deepseek/pro" {
		t.Errorf("small fast = %q, want deepseek/pro", v)
	}
	if v, _ := edit.GetJSON(path, "env.CLAUDE_CODE_SUBAGENT_MODEL"); v != "deepseek/pro" {
		t.Errorf("subagent = %q, want unified deepseek/pro", v)
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Errorf("theme lost: %q", v)
	}
}
