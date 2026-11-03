package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

const (
	pro   = "deepseek/deepseek-pro"
	flash = "deepseek/deepseek-flash"
	lite  = "deepseek/deepseek-lite"
)

// claudeHome sets up an empty home with one ready magpie provider that has
// three models, and returns the home directory.
func claudeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{
		ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k",
		Models: []string{"deepseek-pro", "deepseek-flash", "deepseek-lite"},
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

func claudeSettings(home string) string { return filepath.Join(home, ".claude", "settings.json") }

// checkClaudeEnv compares env variables in settings.json. An empty want value
// means the variable must be absent.
func checkClaudeEnv(t *testing.T, home string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		got, ok := edit.GetJSON(claudeSettings(home), "env."+k)
		if v == "" {
			if ok {
				t.Errorf("%s = %q, want absent", k, got)
			}
			continue
		}
		if !ok || got != v {
			t.Errorf("%s = %q (present %v), want %q", k, got, ok, v)
		}
	}
}

func mustSet(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestClaudeTierNeedsMagpieMainModel(t *testing.T) {
	home := claudeHome(t)
	a := claude(home)

	if err := a.Field("haiku").Set(flash); err == nil {
		t.Fatal("setting a tier before a magpie main model: want error")
	}
	// clearing an unset tier is a no-op
	mustSet(t, a.Field("haiku").Set(""))
	if _, err := os.Stat(claudeSettings(home)); !os.IsNotExist(err) {
		t.Fatalf("settings.json was written: %v", err)
	}

	mustSet(t, a.Field("model").Set(pro))
	if err := a.Field("sonnet").Set("claude-sonnet-4-5"); err == nil {
		t.Fatal("a tier must name a magpie model: want error")
	}
}

func TestClaudeTiersFollowMainModel(t *testing.T) {
	home := claudeHome(t)
	a := claude(home)
	mustSet(t, a.Field("model").Set(pro))

	checkClaudeEnv(t, home, map[string]string{
		"ANTHROPIC_MODEL":                pro,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   pro,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": pro,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  pro,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  pro,
		"ANTHROPIC_SMALL_FAST_MODEL":     pro,
		"CLAUDE_CODE_SUBAGENT_MODEL":     pro,
	})
	if v, _ := edit.GetJSON(claudeSettings(home), "model"); v != pro {
		t.Fatalf("model = %q", v)
	}
	// nothing is pinned yet, so no tier is shown as chosen
	for _, tier := range claudeTiers {
		if got := a.Field(tier).Get(); got != "" {
			t.Errorf("%s shows %q before it is set", tier, got)
		}
	}
}

func TestClaudePinningOneTier(t *testing.T) {
	home := claudeHome(t)
	a := claude(home)
	mustSet(t, a.Field("model").Set(pro))
	mustSet(t, a.Field("haiku").Set(flash))

	checkClaudeEnv(t, home, map[string]string{
		"ANTHROPIC_MODEL":                pro,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   pro,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": pro,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  flash,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  pro,
		"ANTHROPIC_SMALL_FAST_MODEL":     flash,
		"CLAUDE_CODE_SUBAGENT_MODEL":     "", // no longer one model for every subagent
	})
	if got := a.Field("haiku").Get(); got != flash {
		t.Errorf("haiku shows %q, want %q", got, flash)
	}
	if got := a.Field("opus").Get(); got != "" {
		t.Errorf("opus shows %q, want it unset", got)
	}
}

func TestClaudeMainChangeKeepsPinnedTier(t *testing.T) {
	home := claudeHome(t)
	a := claude(home)
	mustSet(t, a.Field("model").Set(pro))
	mustSet(t, a.Field("haiku").Set(flash))

	mustSet(t, a.Field("model").Set(lite))
	checkClaudeEnv(t, home, map[string]string{
		"ANTHROPIC_MODEL":                lite,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   lite,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": lite,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  flash,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  lite,
		"ANTHROPIC_SMALL_FAST_MODEL":     flash,
		"CLAUDE_CODE_SUBAGENT_MODEL":     "",
	})
}

func TestClaudePinnedTierEqualToMainIsKept(t *testing.T) {
	home := claudeHome(t)
	a := claude(home)
	mustSet(t, a.Field("model").Set(lite))
	// pinned to the same model as the main one: it must not start following
	mustSet(t, a.Field("sonnet").Set(lite))
	mustSet(t, a.Field("model").Set(pro))

	checkClaudeEnv(t, home, map[string]string{
		"ANTHROPIC_MODEL":                pro,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   pro,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": lite,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  pro,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  pro,
		"ANTHROPIC_SMALL_FAST_MODEL":     pro,
		"CLAUDE_CODE_SUBAGENT_MODEL":     "",
	})
}

func TestClaudeClearingTierRestoresUnifiedSubagent(t *testing.T) {
	home := claudeHome(t)
	a := claude(home)
	mustSet(t, a.Field("model").Set(pro))
	mustSet(t, a.Field("haiku").Set(flash))
	mustSet(t, a.Field("model").Set(lite))

	mustSet(t, a.Field("haiku").Set("")) // back to following the main model
	checkClaudeEnv(t, home, map[string]string{
		"ANTHROPIC_MODEL":                lite,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   lite,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": lite,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  lite,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  lite,
		"ANTHROPIC_SMALL_FAST_MODEL":     lite,
		"CLAUDE_CODE_SUBAGENT_MODEL":     lite,
	})
	if got := a.Field("haiku").Get(); got != "" {
		t.Errorf("haiku shows %q after it was cleared", got)
	}
}

func TestClaudeClearingMainRemovesMagpieEnv(t *testing.T) {
	home := claudeHome(t)
	if err := os.MkdirAll(filepath.Dir(claudeSettings(home)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claudeSettings(home), []byte("{\n  \"theme\": \"dark\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	mustSet(t, a.Field("model").Set(pro))
	mustSet(t, a.Field("haiku").Set(flash))
	mustSet(t, a.Field("model").Set(""))

	checkClaudeEnv(t, home, map[string]string{
		"ANTHROPIC_BASE_URL":             "",
		"ANTHROPIC_AUTH_TOKEN":           "",
		"ANTHROPIC_MODEL":                "",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  "",
		"ANTHROPIC_SMALL_FAST_MODEL":     "",
		"CLAUDE_CODE_SUBAGENT_MODEL":     "",
	})
	if v, ok := edit.GetJSON(claudeSettings(home), "model"); ok {
		t.Errorf("model = %q, want absent", v)
	}
	if v, _ := edit.GetJSON(claudeSettings(home), "theme"); v != "dark" {
		t.Fatalf("theme = %q, want dark kept", v)
	}

	// routing again starts from scratch: the old pin is not resurrected
	mustSet(t, a.Field("model").Set(pro))
	checkClaudeEnv(t, home, map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": pro, "CLAUDE_CODE_SUBAGENT_MODEL": pro})
}
