package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func TestClaudeTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }

	if err := a.Field("haiku").Set("deepseek/flash"); err == nil {
		t.Fatal("a tier before Claude Code is routed should fail")
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/pro" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "deepseek/pro" || a.Field("haiku").Get() != "" {
		t.Fatalf("tiers should follow the model: %v", a.Values())
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/flash" || env("ANTHROPIC_SMALL_FAST_MODEL") != "deepseek/flash" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatalf("haiku: %v", a.Values())
	}
	if env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "deepseek/pro" || a.Field("haiku").Get() != "deepseek/flash" {
		t.Fatalf("others: %v", a.Values())
	}
	// a new main model carries the tiers that followed it, not haiku
	if err := a.Field("model").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_SONNET_MODEL") != "deepseek/pro" || env("ANTHROPIC_DEFAULT_FABLE_MODEL") != "deepseek/pro" {
		t.Fatalf("sonnet/fable should follow: %v", a.Values())
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/pro" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "deepseek/pro" {
		t.Fatalf("haiku back to the model: %v", a.Values())
	}
	// back to Claude Code as installed
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_FABLE_MODEL") != "" || env("ANTHROPIC_BASE_URL") != "" {
		t.Fatal("reset left env behind")
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatal("theme lost")
	}
}
