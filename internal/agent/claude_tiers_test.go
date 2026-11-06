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

// claudeTierHome sets up a HOME with a deepseek provider (pro, flash) and a
// Claude Code settings file holding only a theme.
func claudeTierHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "chat"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func envOf(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	v, ok := edit.GetJSON(path, "env."+key)
	return v, ok
}

// expectEnv checks every tier variable, the small-fast model, and the
// shared subagent model against want (a missing key is "").
func expectEnv(t *testing.T, path string, want map[string]string) {
	t.Helper()
	for _, k := range []string{
		"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	} {
		got, _ := envOf(t, path, k)
		if got != want[k] {
			t.Errorf("%s = %q, want %q", k, got, want[k])
		}
	}
	if theme, _ := edit.GetJSON(path, "theme"); theme != "dark" {
		t.Errorf("theme = %q, want dark", theme)
	}
}

func TestClaudeTierNeedsMagpieMainModel(t *testing.T) {
	_, path := claudeTierHome(t)
	a := claude(filepath.Dir(filepath.Dir(path)))
	if err := a.Field("haiku").Set("deepseek/flash"); err == nil || !strings.Contains(err.Error(), "main model") {
		t.Fatalf("expected a main-model error, got %v", err)
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatalf("clearing an unrouted tier should do nothing, got %v", err)
	}
	if _, ok := envOf(t, path, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); ok {
		t.Fatal("tier written before routing")
	}
}

func TestClaudeTiers(t *testing.T) {
	home, path := claudeTierHome(t)
	a := claude(home)
	base := gateway.URL()

	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	all := func(main string) map[string]string {
		return map[string]string{
			"ANTHROPIC_MODEL":                main,
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   main,
			"ANTHROPIC_DEFAULT_SONNET_MODEL": main,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  main,
			"ANTHROPIC_DEFAULT_FABLE_MODEL":  main,
			"ANTHROPIC_SMALL_FAST_MODEL":     main,
			"CLAUDE_CODE_SUBAGENT_MODEL":     main,
		}
	}
	expectEnv(t, path, all("deepseek/pro"))
	if v, _ := edit.GetJSON(path, "env.ANTHROPIC_BASE_URL"); v != base {
		t.Fatalf("base url = %q", v)
	}

	// pin haiku: only haiku and the small-fast model move, and the shared
	// subagent model is withdrawn
	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	pinned := all("deepseek/pro")
	pinned["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = "deepseek/flash"
	pinned["ANTHROPIC_SMALL_FAST_MODEL"] = "deepseek/flash"
	pinned["CLAUDE_CODE_SUBAGENT_MODEL"] = ""
	expectEnv(t, path, pinned)
	if got := a.Field("haiku").Get(); got != "deepseek/flash" {
		t.Fatalf("haiku shown as %q", got)
	}
	if got := a.Field("opus").Get(); got != "" {
		t.Fatalf("following opus shown as %q, want empty", got)
	}

	// changing the main model moves the followers and keeps haiku
	if err := a.Field("model").Set("deepseek/chat"); err != nil {
		t.Fatal(err)
	}
	moved := all("deepseek/chat")
	moved["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = "deepseek/flash"
	moved["ANTHROPIC_SMALL_FAST_MODEL"] = "deepseek/flash"
	moved["CLAUDE_CODE_SUBAGENT_MODEL"] = ""
	expectEnv(t, path, moved)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	pinned = all("deepseek/pro")
	pinned["ANTHROPIC_DEFAULT_HAIKU_MODEL"] = "deepseek/flash"
	pinned["ANTHROPIC_SMALL_FAST_MODEL"] = "deepseek/flash"
	pinned["CLAUDE_CODE_SUBAGENT_MODEL"] = ""
	expectEnv(t, path, pinned)

	// clearing haiku puts every tier back on the main model and restores
	// the shared subagent model
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	expectEnv(t, path, all("deepseek/pro"))
	if got := a.Field("haiku").Get(); got != "" {
		t.Fatalf("cleared haiku still shown as %q", got)
	}

	// a tier that is not a magpie model is refused
	if err := a.Field("fable").Set("gpt-9"); err == nil {
		t.Fatal("non-magpie tier accepted")
	}

	// removing the main model clears every related variable and keeps theme
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	expectEnv(t, path, map[string]string{})
	for _, k := range claudeEnv {
		if _, ok := envOf(t, path, k); ok {
			t.Errorf("%s still set after reset", k)
		}
	}
}

func TestClaudeFableTierVariable(t *testing.T) {
	home, path := claudeTierHome(t)
	a := claude(home)
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("fable").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if got, _ := envOf(t, path, "ANTHROPIC_DEFAULT_FABLE_MODEL"); got != "deepseek/flash" {
		t.Fatalf("fable env = %q", got)
	}
}
