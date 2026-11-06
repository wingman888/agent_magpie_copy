package profile

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// A profile applies the main model before the other Claude Code fields: a tier
// is only settable once Claude Code is routed through magpie by its main model.
func TestApplySetsMainModelBeforeTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}

	n, err := Apply(Profile{"claude.haiku": "deepseek/flash", "claude.model": "deepseek/pro"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n != 2 {
		t.Fatalf("changed %d, want 2", n)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/flash" {
		t.Fatalf("haiku = %q", v)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_OPUS_MODEL"); v != "deepseek/pro" {
		t.Fatalf("opus follows main = %q", v)
	}
	if a, err := agent.Find("claude"); err != nil || a.Field("haiku").Get() != "deepseek/flash" {
		t.Fatalf("haiku field: %v", err)
	}
}
