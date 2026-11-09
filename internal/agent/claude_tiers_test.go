package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// tierSetupHome gives a fresh home with one magpie provider and the Claude
// agent bound to that home.
func tierSetupHome(t *testing.T) (string, string, *Agent) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "max"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	return home, path, claude(home)
}

// tierEnvOf reads one env var of the Claude settings file; ok is false when absent.
func tierEnvOf(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(path, "env."+key)
}

// tierExpect checks the tier, small fast and subagent variables in one go.
func tierExpect(t *testing.T, path string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		got, ok := tierEnvOf(t, path, k)
		if v == "<absent>" {
			if ok {
				t.Fatalf("%s: want absent, got %q", k, got)
			}
			continue
		}
		if !ok || got != v {
			t.Fatalf("%s: want %q, got %q (present=%v)", k, v, got, ok)
		}
	}
}

func TestTierFollowsMainModel(t *testing.T) {
	_, path, a := tierSetupHome(t)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	tierExpect(t, path, map[string]string{
		"ANTHROPIC_MODEL":                "deepseek/pro",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek/pro",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/pro",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek/pro",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL":     "deepseek/pro",
		"CLAUDE_CODE_SUBAGENT_MODEL":     "deepseek/pro",
	})
	if a.Field("haiku").Get() != "" {
		t.Fatalf("unpicked tier should read empty, got %q", a.Field("haiku").Get())
	}
}

func TestTierHaikuPickCancelsUniformSubagent(t *testing.T) {
	_, path, a := tierSetupHome(t)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	tierExpect(t, path, map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek/flash",
		"ANTHROPIC_SMALL_FAST_MODEL":     "deepseek/flash",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek/pro",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/pro",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "deepseek/pro",
		"CLAUDE_CODE_SUBAGENT_MODEL":     "<absent>",
	})
	if a.Field("haiku").Get() != "deepseek/flash" {
		t.Fatalf("haiku shown: %q", a.Field("haiku").Get())
	}
}

func TestTierKeepsPickWhenMainChanges(t *testing.T) {
	_, path, a := tierSetupHome(t)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("deepseek/max"); err != nil {
		t.Fatal(err)
	}
	tierExpect(t, path, map[string]string{
		"ANTHROPIC_MODEL":                "deepseek/max",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "deepseek/flash",
		"ANTHROPIC_SMALL_FAST_MODEL":     "deepseek/flash",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek/max",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/max",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "deepseek/max",
		"CLAUDE_CODE_SUBAGENT_MODEL":     "<absent>",
	})
}

func TestTierHaikuResetRestoresUniform(t *testing.T) {
	_, path, a := tierSetupHome(t)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	tierExpect(t, path, map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/pro",
		"ANTHROPIC_SMALL_FAST_MODEL":    "deepseek/pro",
		"CLAUDE_CODE_SUBAGENT_MODEL":    "deepseek/pro",
	})
	if a.Field("haiku").Get() != "" {
		t.Fatalf("haiku should be unlisted again, got %q", a.Field("haiku").Get())
	}
}

func TestTierNeedsMagpieMainModel(t *testing.T) {
	_, path, a := tierSetupHome(t)
	if err := a.Field("haiku").Set("deepseek/flash"); err == nil {
		t.Fatal("expected an error before a magpie main model is set")
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatalf("clearing an unrouted tier should be a no-op: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("clearing an unrouted tier must not write settings")
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("claude-haiku-4"); err == nil {
		t.Fatal("expected an error for a model magpie does not provide")
	}
}

func TestTierFableWrite(t *testing.T) {
	_, path, a := tierSetupHome(t)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("fable").Set("deepseek/max"); err != nil {
		t.Fatal(err)
	}
	tierExpect(t, path, map[string]string{
		"ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/max",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/pro",
	})
}

func TestTierClearMainRemovesEnvKeepsOthers(t *testing.T) {
	_, path, a := tierSetupHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark", "env": {"KEEP_ME": "1"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	tierExpect(t, path, map[string]string{
		"ANTHROPIC_MODEL":               "<absent>",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "<absent>",
		"ANTHROPIC_DEFAULT_FABLE_MODEL": "<absent>",
		"ANTHROPIC_SMALL_FAST_MODEL":    "<absent>",
		"CLAUDE_CODE_SUBAGENT_MODEL":    "<absent>",
		"KEEP_ME":                       "1",
	})
	if v, ok := edit.GetJSON(path, "theme"); !ok || v != "dark" {
		t.Fatalf("theme not preserved: %q %v", v, ok)
	}
	if a.Field("haiku").Get() != "" {
		t.Fatal("haiku pick should be forgotten after clearing main model")
	}
}
