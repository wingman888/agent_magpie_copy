package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// TestFixClaudePinnedTierSurvivesMainChanges covers a tier picked on its own:
// it keeps its model across main-model changes, including when the pinned
// model equals the main model at the time of the pick.
func TestFixClaudePinnedTierSurvivesMainChanges(t *testing.T) {
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

	mustSet("model", "deepseek/pro")
	mustSet("haiku", "deepseek/flash")
	mustSet("model", "deepseek/flash") // haiku's model becomes main
	mustSet("model", "deepseek/lite")  // and then main moves on
	if got := env("ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/flash" {
		t.Errorf("pinned haiku after two main changes = %q, want deepseek/flash", got)
	}
	if got := env("ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "deepseek/lite" {
		t.Errorf("following opus = %q, want deepseek/lite", got)
	}
	if got := env("ANTHROPIC_SMALL_FAST_MODEL"); got != "deepseek/flash" {
		t.Errorf("small fast model = %q, want deepseek/flash", got)
	}
	if got := env("CLAUDE_CODE_SUBAGENT_MODEL"); got != "" {
		t.Errorf("subagent model written while haiku is pinned: %q", got)
	}
	if got := a.Field("haiku").Get(); got != "deepseek/flash" {
		t.Errorf("haiku shown as %q, want deepseek/flash", got)
	}

	// haiku picked to the same model as main: it must stay pinned
	mustSet("haiku", "deepseek/lite")
	mustSet("model", "deepseek/pro")
	if got := env("ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/lite" {
		t.Errorf("haiku picked equal to main, then main changed: got %q, want deepseek/lite", got)
	}
	if got := env("ANTHROPIC_DEFAULT_SONNET_MODEL"); got != "deepseek/pro" {
		t.Errorf("following sonnet = %q, want deepseek/pro", got)
	}

	// clearing haiku makes it follow again and restores the unified subagent model
	mustSet("haiku", "")
	if got := env("ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/pro" {
		t.Errorf("haiku after clearing = %q, want deepseek/pro", got)
	}
	if got := env("CLAUDE_CODE_SUBAGENT_MODEL"); got != "deepseek/pro" {
		t.Errorf("subagent model after clearing = %q, want deepseek/pro", got)
	}

	// restoring Claude Code clears pins too, so a later route starts clean
	mustSet("model", "deepseek/flash")
	mustSet("haiku", "deepseek/lite")
	mustSet("model", "")
	mustSet("model", "deepseek/pro")
	if got := env("ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/pro" {
		t.Errorf("haiku after restore and re-route = %q, want deepseek/pro", got)
	}
	if got, _ := edit.GetJSON(settings, "theme"); got != "dark" {
		t.Errorf("theme lost: %q", got)
	}
}
