package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// claudeHome sets up a home with one ready provider (deepseek/pro, flash,
// other) and a Claude Code settings file that keeps the user's theme.
func claudeHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "other"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func claudeEnvVal(t *testing.T, path, k string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, "env."+k)
	return v
}

func claudeEnvSet(t *testing.T, path, k string) bool {
	t.Helper()
	_, ok := edit.GetJSON(path, "env."+k)
	return ok
}

func wantTiers(t *testing.T, path string, main string, tiers map[string]string, small string, subagent bool) {
	t.Helper()
	defaults := map[string]string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   tiers["opus"],
		"ANTHROPIC_DEFAULT_SONNET_MODEL": tiers["sonnet"],
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  tiers["haiku"],
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  tiers["fable"],
	}
	for k, want := range defaults {
		if got := claudeEnvVal(t, path, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if got := claudeEnvVal(t, path, "ANTHROPIC_MODEL"); got != main {
		t.Errorf("ANTHROPIC_MODEL = %q, want %q", got, main)
	}
	if got := claudeEnvVal(t, path, "ANTHROPIC_SMALL_FAST_MODEL"); got != small {
		t.Errorf("ANTHROPIC_SMALL_FAST_MODEL = %q, want %q", got, small)
	}
	if subagent {
		if got := claudeEnvVal(t, path, "CLAUDE_CODE_SUBAGENT_MODEL"); got != main {
			t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL = %q, want %q", got, main)
		}
	} else if claudeEnvSet(t, path, "CLAUDE_CODE_SUBAGENT_MODEL") {
		t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL still set to %q", claudeEnvVal(t, path, "CLAUDE_CODE_SUBAGENT_MODEL"))
	}
}

func TestClaudeTiersFollowMain(t *testing.T) {
	_, path := claudeHome(t)
	a := claude(filepath.Dir(filepath.Dir(path)))
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"opus": "deepseek/pro", "sonnet": "deepseek/pro", "haiku": "deepseek/pro", "fable": "deepseek/pro"}
	wantTiers(t, path, "deepseek/pro", all, "deepseek/pro", true)
	for _, k := range []string{"opus", "sonnet", "haiku", "fable"} {
		if got := a.Field(k).Get(); got != "" {
			t.Errorf("%s shown as %q while following", k, got)
		}
		if !a.Field(k).Optional {
			t.Errorf("%s should be optional", k)
		}
	}
}

func TestClaudeHaikuPinnedThenMainChanges(t *testing.T) {
	_, path := claudeHome(t)
	a := claude(filepath.Dir(filepath.Dir(path)))
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	wantTiers(t, path, "deepseek/pro",
		map[string]string{"opus": "deepseek/pro", "sonnet": "deepseek/pro", "haiku": "deepseek/flash", "fable": "deepseek/pro"},
		"deepseek/flash", false)
	if got := a.Field("haiku").Get(); got != "deepseek/flash" {
		t.Fatalf("haiku shown as %q", got)
	}
	if got := a.Field("opus").Get(); got != "" {
		t.Fatalf("opus shown as %q while following", got)
	}

	// the main model changes: the followers move, haiku stays
	if err := a.Field("model").Set("deepseek/other"); err != nil {
		t.Fatal(err)
	}
	wantTiers(t, path, "deepseek/other",
		map[string]string{"opus": "deepseek/other", "sonnet": "deepseek/other", "haiku": "deepseek/flash", "fable": "deepseek/other"},
		"deepseek/flash", false)
	if got := a.Field("haiku").Get(); got != "deepseek/flash" {
		t.Fatalf("haiku lost its pin: %q", got)
	}

	// clearing haiku puts it back to following and restores the unified subagent model
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	wantTiers(t, path, "deepseek/other",
		map[string]string{"opus": "deepseek/other", "sonnet": "deepseek/other", "haiku": "deepseek/other", "fable": "deepseek/other"},
		"deepseek/other", true)
	if got := a.Field("haiku").Get(); got != "" {
		t.Fatalf("haiku shown as %q after clearing", got)
	}
}

func TestClaudeTierNeedsMagpieMain(t *testing.T) {
	_, path := claudeHome(t)
	a := claude(filepath.Dir(filepath.Dir(path)))
	before, _ := os.ReadFile(path)

	if err := a.Field("haiku").Set("deepseek/flash"); err == nil || !strings.Contains(err.Error(), "magpie") {
		t.Fatalf("setting a tier before routing: err = %v, want an error asking for a magpie main model", err)
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatalf("clearing a tier before routing: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatalf("settings changed:\n%s", after)
	}
}

func TestClaudeTierRejectsNonMagpieModel(t *testing.T) {
	_, path := claudeHome(t)
	a := claude(filepath.Dir(filepath.Dir(path)))
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("claude-3-haiku"); err == nil {
		t.Fatal("a model magpie does not provide was accepted")
	}
	if got := a.Field("haiku").Get(); got != "" {
		t.Fatalf("haiku pinned to %q after rejection", got)
	}
}

func TestClaudeMainClearedRemovesTiersKeepsTheme(t *testing.T) {
	_, path := claudeHome(t)
	a := claude(filepath.Dir(filepath.Dir(path)))
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("fable").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	for _, k := range claudeEnv {
		if claudeEnvSet(t, path, k) {
			t.Errorf("env.%s still set to %q", k, claudeEnvVal(t, path, k))
		}
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Fatalf("theme = %q, want dark", v)
	}
	if got := a.Field("fable").Get(); got != "" {
		t.Fatalf("fable pin survived: %q", got)
	}
	if b := stashLoad(); b[tierKey("fable")] != "" {
		t.Fatalf("fable pin still stashed: %q", b[tierKey("fable")])
	}
}

func TestClaudeSecondaryPinVisibleWhileRouted(t *testing.T) {
	// a pin made while routed is shown by the field and by Values()
	_, path := claudeHome(t)
	a := claude(filepath.Dir(filepath.Dir(path)))
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("sonnet").Set("deepseek/other"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); v != gateway.URL() {
		t.Fatalf("base url = %q", v)
	}
	if got := a.Values()["sonnet"]; got != "deepseek/other" {
		t.Fatalf("Values()[sonnet] = %q", got)
	}
}
