package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// TestS3ClaudeTiersFollowAndPin walks Claude Code's tiers through the gateway:
// tiers follow the main model until one is picked on its own, and clearing
// the main model removes every magpie setting but keeps the others.
func TestS3ClaudeTiersFollowAndPin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "k", Models: []string{"pro", "flash", "lite"}}); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	env := func(k string) string { v, _ := edit.GetJSON(settings, "env."+k); return v }
	mustSet := func(key, v string) {
		t.Helper()
		if err := a.Field(key).Set(v); err != nil {
			t.Fatalf("set %s=%q: %v", key, v, err)
		}
	}
	check := func(step string, want map[string]string) {
		t.Helper()
		for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
			if got := env(k); got != want[k] {
				t.Errorf("%s: %s = %q, want %q", step, k, got, want[k])
			}
		}
	}

	// not routed through magpie yet: a tier is refused, clearing is a no-op
	if err := a.Field("haiku").Set("deepseek/flash"); err == nil {
		t.Error("tier accepted before the main model is set")
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Errorf("clearing a tier before routing: %v", err)
	}

	mustSet("model", "deepseek/pro")
	check("main pro", map[string]string{"ANTHROPIC_MODEL": "deepseek/pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/pro", "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/pro", "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL": "deepseek/pro", "CLAUDE_CODE_SUBAGENT_MODEL": "deepseek/pro"})
	if got := a.Field("haiku").Get(); got != "" {
		t.Errorf("following tier shown as %q", got)
	}
	if err := a.Field("opus").Set("claude-opus-5"); err == nil {
		t.Error("a model magpie does not provide was accepted for a tier")
	}

	mustSet("haiku", "deepseek/flash")
	check("haiku pinned", map[string]string{"ANTHROPIC_MODEL": "deepseek/pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/pro", "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/flash", "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL": "deepseek/flash"})
	if got := a.Field("haiku").Get(); got != "deepseek/flash" {
		t.Errorf("pinned haiku shown as %q", got)
	}
	if got := a.Field("opus").Get(); got != "" {
		t.Errorf("following opus shown as %q", got)
	}

	// changing the main model moves the followers and keeps the pinned tier
	mustSet("model", "deepseek/lite")
	check("main lite", map[string]string{"ANTHROPIC_MODEL": "deepseek/lite",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/lite", "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/lite",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/flash", "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/lite",
		"ANTHROPIC_SMALL_FAST_MODEL": "deepseek/flash"})

	// back to following: every tier equals main, so the subagent model is unified again
	mustSet("haiku", "")
	check("haiku follows", map[string]string{"ANTHROPIC_MODEL": "deepseek/lite",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/lite", "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/lite",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/lite", "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/lite",
		"ANTHROPIC_SMALL_FAST_MODEL": "deepseek/lite", "CLAUDE_CODE_SUBAGENT_MODEL": "deepseek/lite"})

	// a pinned fable drops the unified subagent model again
	mustSet("fable", "deepseek/flash")
	check("fable pinned", map[string]string{"ANTHROPIC_MODEL": "deepseek/lite",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/lite", "ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/lite",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/lite", "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/flash",
		"ANTHROPIC_SMALL_FAST_MODEL": "deepseek/lite"})

	// restoring Claude Code as installed removes all of it and keeps the rest
	mustSet("model", "")
	check("restored", map[string]string{})
	if got, _ := edit.GetJSON(settings, "theme"); got != "dark" {
		t.Errorf("theme lost: %q", got)
	}
}
