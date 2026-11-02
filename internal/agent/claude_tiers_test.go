package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

func claudeTierSetup(t *testing.T) (*Agent, string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "sk-x", Models: []string{"pro", "flash", "other"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"theme":"dark"}`), 0o644)
	return claude(home), path
}

func TestClaudeTiers(t *testing.T) {
	a, path := claudeTierSetup(t)
	env := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	want := func(label string, kv map[string]string) {
		t.Helper()
		for k, v := range kv {
			if got := env(k); got != v {
				t.Errorf("%s: %s = %q, want %q", label, k, got, v)
			}
		}
	}
	all := []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "ANTHROPIC_MODEL"}

	// not routed yet: setting a tier fails, clearing is a no-op
	if err := a.Field("haiku").Set("deepseek/flash"); err == nil || !strings.Contains(err.Error(), "magpie") {
		t.Fatalf("unrouted tier set: %v", err)
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}

	if err := a.Fields[0].Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, k := range all {
		m[k] = "deepseek/pro"
	}
	want("routed", m)

	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	want("haiku", map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/flash", "ANTHROPIC_SMALL_FAST_MODEL": "deepseek/flash",
		"ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/pro", "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/pro", "ANTHROPIC_MODEL": "deepseek/pro"})
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Error("subagent model still forced")
	}
	if a.Field("haiku").Get() != "deepseek/flash" || a.Field("opus").Get() != "" {
		t.Error("tier getters")
	}

	// changing the main model moves followers, keeps haiku
	if err := a.Fields[0].Set("deepseek/other"); err != nil {
		t.Fatal(err)
	}
	want("main change", map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "deepseek/flash", "ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek/other",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "deepseek/other", "ANTHROPIC_DEFAULT_FABLE_MODEL": "deepseek/other", "ANTHROPIC_MODEL": "deepseek/other"})
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != "" {
		t.Error("subagent model forced after main change")
	}

	if err := a.Field("fable").Set("not-a-model"); err == nil {
		t.Error("non-magpie tier model accepted")
	}

	// clearing restores the unified setup
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	m = map[string]string{}
	for _, k := range all {
		m[k] = "deepseek/other"
	}
	want("cleared", m)

	// main cleared: everything goes, theme stays
	if err := a.Fields[0].Set(""); err != nil {
		t.Fatal(err)
	}
	for _, k := range append(all, "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN") {
		if env(k) != "" {
			t.Errorf("%s survived", k)
		}
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Errorf("theme lost: %q", v)
	}
}
