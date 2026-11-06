package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func TestClaudeTiersL3x(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}

	a := claude(home)
	field := func(k string) *Field {
		t.Helper()
		f := a.Field(k)
		if f == nil {
			t.Fatalf("claude has no field %q", k)
		}
		return f
	}
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }

	// check asserts that each env var in pairs (name, value, name, value…)
	// holds its value, and that each name in absent is gone
	check := func(label string, pairs []string, absent ...string) {
		t.Helper()
		for i := 0; i+1 < len(pairs); i += 2 {
			if got := env(pairs[i]); got != pairs[i+1] {
				t.Fatalf("%s: %s = %q, want %q", label, pairs[i], got, pairs[i+1])
			}
		}
		for _, k := range absent {
			if got := env(k); got != "" {
				t.Fatalf("%s: %s = %q, want it gone", label, k, got)
			}
		}
	}

	// tiers need a main model magpie provides first
	if err := field("haiku").Set("deepseek/flash"); err == nil {
		t.Fatal("tier set before any main model")
	}
	if err := field("haiku").Set(""); err != nil {
		t.Fatalf("clearing a tier with no main model should be a no-op: %v", err)
	}

	// a main model: all four tiers follow it, and the subagent model is unified
	if err := field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	check("main", []string{
		"ANTHROPIC_MODEL", "deepseek/pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "deepseek/pro",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "deepseek/pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "deepseek/pro",
		"ANTHROPIC_DEFAULT_FABLE_MODEL", "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL", "deepseek/pro",
		"CLAUDE_CODE_SUBAGENT_MODEL", "deepseek/pro",
	})
	if v := field("haiku").Get(); v != "" {
		t.Fatalf("a following tier shows %q, want nothing", v)
	}
	if v := a.Values()["haiku"]; v != "" {
		t.Fatalf("values: following haiku = %q", v)
	}

	// haiku on its own: it and the small model change, the others stay, and
	// subagents are no longer forced to one model
	if err := field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	check("haiku pinned", []string{
		"ANTHROPIC_MODEL", "deepseek/pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "deepseek/pro",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "deepseek/pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "deepseek/flash",
		"ANTHROPIC_DEFAULT_FABLE_MODEL", "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL", "deepseek/flash",
	}, "CLAUDE_CODE_SUBAGENT_MODEL")
	if v := field("haiku").Get(); v != "deepseek/flash" {
		t.Fatalf("pinned haiku shows %q", v)
	}
	if v := field("opus").Get(); v != "" {
		t.Fatalf("following opus shows %q", v)
	}

	// a new main model: the tiers that followed it follow the new one, the
	// pinned haiku stays
	if err := field("model").Set("deepseek/max"); err != nil {
		t.Fatal(err)
	}
	check("main changed", []string{
		"ANTHROPIC_MODEL", "deepseek/max",
		"ANTHROPIC_DEFAULT_OPUS_MODEL", "deepseek/max",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "deepseek/max",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "deepseek/flash",
		"ANTHROPIC_DEFAULT_FABLE_MODEL", "deepseek/max",
		"ANTHROPIC_SMALL_FAST_MODEL", "deepseek/flash",
	}, "CLAUDE_CODE_SUBAGENT_MODEL")
	if v := field("haiku").Get(); v != "deepseek/flash" {
		t.Fatalf("haiku after a main change shows %q", v)
	}

	// a value magpie does not provide is refused, and nothing changes
	if err := field("sonnet").Set("nope"); err == nil {
		t.Fatal("a value magpie does not provide was accepted")
	}
	check("after refusal", []string{"ANTHROPIC_DEFAULT_SONNET_MODEL", "deepseek/max"})

	// clearing haiku puts it back to the main model; with every tier equal
	// again the subagent model is unified
	if err := field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	check("haiku cleared", []string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "deepseek/max",
		"ANTHROPIC_SMALL_FAST_MODEL", "deepseek/max",
		"CLAUDE_CODE_SUBAGENT_MODEL", "deepseek/max",
	})
	if v := field("haiku").Get(); v != "" {
		t.Fatalf("cleared haiku shows %q", v)
	}

	// restoring Claude Code removes every env var magpie set and keeps the rest
	if err := field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	check("restored", nil, "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN")
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatalf("theme lost: %q", v)
	}
	if err := field("haiku").Set(""); err != nil {
		t.Fatalf("clearing a tier after restore should be a no-op: %v", err)
	}
	if err := field("haiku").Set("deepseek/flash"); err == nil {
		t.Fatal("tier set after restore, with no main model")
	}
}
