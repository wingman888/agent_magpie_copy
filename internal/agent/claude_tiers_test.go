package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func tierSetup(t *testing.T) (*Agent, func(string) string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k",
		Models: []string{"pro", "flash", "mid"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := claude(home)
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	return a, env
}

func TestClaudeTiers(t *testing.T) {
	a, env := tierSetup(t)
	haiku := a.Field("haiku")
	if err := haiku.Set("deepseek/flash"); err == nil || !strings.Contains(err.Error(), "main model") {
		t.Fatalf("tier before routing: %v", err)
	}
	if err := haiku.Set(""); err != nil {
		t.Fatalf("clearing should be a no-op: %v", err)
	}
	main := a.Field("model")
	if err := main.Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	all := []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"}
	for _, k := range all {
		if env(k) != "deepseek/pro" {
			t.Fatalf("%s = %q", k, env(k))
		}
	}
	if haiku.Get() != "" {
		t.Fatalf("follower shown: %q", haiku.Get())
	}
	if err := haiku.Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/flash" || env("ANTHROPIC_SMALL_FAST_MODEL") != "deepseek/flash" ||
		env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "deepseek/pro" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" || haiku.Get() != "deepseek/flash" {
		t.Fatal("haiku override not applied")
	}
	if err := a.Field("opus").Set("nonsense"); err == nil {
		t.Fatal("non-magpie model accepted")
	}
	if err := main.Set("deepseek/mid"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_OPUS_MODEL") != "deepseek/mid" || env("ANTHROPIC_DEFAULT_FABLE_MODEL") != "deepseek/mid" ||
		env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/flash" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Fatal("main change did not move followers only")
	}
	if err := haiku.Set(""); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_HAIKU_MODEL") != "deepseek/mid" || env("ANTHROPIC_SMALL_FAST_MODEL") != "deepseek/mid" || env("CLAUDE_CODE_SUBAGENT_MODEL") != "deepseek/mid" {
		t.Fatal("not unified again")
	}
	if err := a.Field("fable").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if env("ANTHROPIC_DEFAULT_FABLE_MODEL") != "deepseek/pro" {
		t.Fatal("fable")
	}
	if err := main.Set(""); err != nil {
		t.Fatal(err)
	}
	for _, k := range all {
		if env(k) != "" {
			t.Fatalf("%s survived: %q", k, env(k))
		}
	}
	if env("ANTHROPIC_BASE_URL") != "" {
		t.Fatal("base url survived")
	}
	b, _ := os.ReadFile(a.Path)
	if !strings.Contains(string(b), `"theme"`) {
		t.Fatalf("theme lost: %s", b)
	}
}
