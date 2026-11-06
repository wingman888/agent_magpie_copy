package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// claudeTestHome sets up a temp home with a ready magpie provider and Claude
// Code's settings file holding the given JSON.
func claudeTestHome(t *testing.T, settings string) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "k", Models: []string{"pro", "flash", "v4"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if settings != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return claude(home), path
}

// expectClaudeEnv checks the env entries in want hold their values and absent are gone.
func expectClaudeEnv(t *testing.T, path string, want map[string]string, absent ...string) {
	t.Helper()
	for k, v := range want {
		if got, _ := edit.GetJSON(path, "env."+k); got != v {
			t.Errorf("env.%s = %q, want %q", k, got, v)
		}
	}
	for _, k := range absent {
		if got, _ := edit.GetJSON(path, "env."+k); got != "" {
			t.Errorf("env.%s = %q, want it removed", k, got)
		}
	}
}

func TestClaudeTierEnvNames(t *testing.T) {
	if got := tierEnv("fable"); got != "ANTHROPIC_DEFAULT_FABLE_MODEL" {
		t.Fatalf("fable env = %s", got)
	}
	for _, k := range []string{"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		found := false
		for _, c := range claudeEnv {
			found = found || c == k
		}
		if !found {
			t.Errorf("%s is not cleared with the others", k)
		}
	}
}

func TestClaudeTiersFollowThenSeparate(t *testing.T) {
	a, path := claudeTestHome(t, `{"theme": "dark"}`)
	if !a.Field("haiku").Explicit || a.Field("model").Explicit {
		t.Fatal("tiers must be explicit-only fields, the main model is not")
	}

	// main model set: every tier follows it, subagents share it
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	expectClaudeEnv(t, path, map[string]string{
		"ANTHROPIC_MODEL":                "deepseek/pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek/pro",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek/pro",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL":     "deepseek/pro",
		"CLAUDE_CODE_SUBAGENT_MODEL":     "deepseek/pro",
	})
	if v := a.Field("haiku").Get(); v != "" {
		t.Fatalf("a following tier shows %q, want nothing", v)
	}

	// haiku set apart: it and the small fast model change, the subagent model is dropped
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	expectClaudeEnv(t, path, map[string]string{
		"ANTHROPIC_MODEL":               "deepseek/pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":  "deepseek/pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/flash",
		"ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL":    "deepseek/flash",
	}, "CLAUDE_CODE_SUBAGENT_MODEL")
	if v := a.Field("haiku").Get(); v != "deepseek/flash" {
		t.Fatalf("haiku shows %q", v)
	}

	// main model changes: the following tiers move, haiku keeps its own model
	if err := a.Field("model").Set("deepseek/v4"); err != nil {
		t.Fatal(err)
	}
	expectClaudeEnv(t, path, map[string]string{
		"ANTHROPIC_MODEL":                "deepseek/v4",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek/v4",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/v4",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek/flash",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "deepseek/v4",
		"ANTHROPIC_SMALL_FAST_MODEL":     "deepseek/flash",
	}, "CLAUDE_CODE_SUBAGENT_MODEL")

	// clearing the tier brings it back to the main model; all agree again
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	expectClaudeEnv(t, path, map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/v4",
		"ANTHROPIC_SMALL_FAST_MODEL":    "deepseek/v4",
		"CLAUDE_CODE_SUBAGENT_MODEL":    "deepseek/v4",
	})
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatalf("theme lost: %q", v)
	}
}

func TestClaudeTierNeedsMagpieMainModel(t *testing.T) {
	a, path := claudeTestHome(t, `{"env": {"ANTHROPIC_MODEL": "claude-sonnet-5"}}`)
	if err := a.Field("sonnet").Set("deepseek/flash"); err == nil {
		t.Fatal("tier set while Claude Code is not using magpie")
	}
	if got, _ := edit.GetJSON(path, "env.ANTHROPIC_DEFAULT_SONNET_MODEL"); got != "" {
		t.Fatalf("tier written anyway: %q", got)
	}
	if err := a.Field("sonnet").Set(""); err != nil {
		t.Fatalf("clearing a tier without magpie should do nothing: %v", err)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("sonnet").Set("claude-opus-5-5"); err == nil {
		t.Fatal("a model magpie does not provide was accepted for a tier")
	}
}

func TestClearingMainModelRemovesClaudeEnv(t *testing.T) {
	a, path := claudeTestHome(t, `{"theme": "dark"}`)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("fable").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	absent := append([]string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"}, claudeEnv...)
	expectClaudeEnv(t, path, nil, absent...)
	if v, _ := edit.GetJSON(path, "model"); v != "" {
		t.Fatalf("model left behind: %q", v)
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatalf("theme lost: %q", v)
	}
}
