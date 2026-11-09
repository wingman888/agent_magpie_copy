package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// tierSetup points HOME and the config dir at a temp dir with a DeepSeek
// provider carrying three models, and writes a Claude Code settings file.
func tierSetup(t *testing.T) (home, settings string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k",
		Models: []string{"pro", "flash", "chat"}}); err != nil {
		t.Fatal(err)
	}
	settings = filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, settings
}

// tierEnv reads one env var from the Claude Code settings file.
func tierEnv(t *testing.T, settings, key string) string {
	t.Helper()
	v, _ := edit.GetJSON(settings, "env."+key)
	return v
}

// tierField returns a claude field by key or fails the test.
func tierField(t *testing.T, a *Agent, key string) *Field {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	return f
}

func TestTierClaudeFollowAndPin(t *testing.T) {
	_, settings := tierSetup(t)
	a := claude(filepath.Dir(filepath.Dir(settings)))

	if err := tierField(t, a, "model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		if v := tierEnv(t, settings, k); v != "deepseek/pro" {
			t.Fatalf("%s = %q, want deepseek/pro", k, v)
		}
	}
	if err := tierField(t, a, "haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if v := tierEnv(t, settings, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/flash" {
		t.Fatalf("haiku = %q", v)
	}
	if v := tierEnv(t, settings, "ANTHROPIC_SMALL_FAST_MODEL"); v != "deepseek/flash" {
		t.Fatalf("small fast = %q", v)
	}
	if v := tierEnv(t, settings, "ANTHROPIC_DEFAULT_OPUS_MODEL"); v != "deepseek/pro" {
		t.Fatalf("opus = %q, should follow main", v)
	}
	if v := tierEnv(t, settings, "CLAUDE_CODE_SUBAGENT_MODEL"); v != "" {
		t.Fatalf("subagent model still forced: %q", v)
	}
	if got := tierField(t, a, "haiku").Get(); got != "deepseek/flash" {
		t.Fatalf("haiku Get = %q", got)
	}
	if got := tierField(t, a, "opus").Get(); got != "" {
		t.Fatalf("unpinned opus Get = %q, want empty", got)
	}

	// changing the main model moves the followers and keeps the pin
	if err := tierField(t, a, "model").Set("deepseek/chat"); err != nil {
		t.Fatal(err)
	}
	if v := tierEnv(t, settings, "ANTHROPIC_DEFAULT_OPUS_MODEL"); v != "deepseek/chat" {
		t.Fatalf("opus after main change = %q", v)
	}
	if v := tierEnv(t, settings, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/flash" {
		t.Fatalf("haiku after main change = %q", v)
	}

	// clearing the pin restores the unified state
	if err := tierField(t, a, "haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	if v := tierEnv(t, settings, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/chat" {
		t.Fatalf("haiku after clear = %q", v)
	}
	if v := tierEnv(t, settings, "CLAUDE_CODE_SUBAGENT_MODEL"); v != "deepseek/chat" {
		t.Fatalf("subagent after clear = %q", v)
	}

	// clearing the main model removes every related env var and keeps theme
	if err := tierField(t, a, "model").Set(""); err != nil {
		t.Fatal(err)
	}
	for _, k := range claudeEnv {
		if v := tierEnv(t, settings, k); v != "" {
			t.Fatalf("%s left behind: %q", k, v)
		}
	}
	if v, _ := edit.GetJSON(settings, "theme"); v != "dark" {
		t.Fatalf("theme lost: %q", v)
	}
}

func TestTierClaudeRequiresRouting(t *testing.T) {
	_, settings := tierSetup(t)
	a := claude(filepath.Dir(filepath.Dir(settings)))
	before, _ := os.ReadFile(settings)

	if err := tierField(t, a, "haiku").Set("deepseek/flash"); err == nil {
		t.Fatal("setting a tier before routing through magpie should fail")
	}
	if err := tierField(t, a, "haiku").Set(""); err != nil {
		t.Fatalf("clearing an unrouted tier should be a no-op: %v", err)
	}
	after, _ := os.ReadFile(settings)
	if string(before) != string(after) {
		t.Fatalf("settings changed:\n%s", after)
	}

	if err := tierField(t, a, "model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := tierField(t, a, "opus").Set("not-a-magpie-model"); err == nil {
		t.Fatal("a tier must be a magpie model")
	}
}

func TestTierClaudeOverrideFlags(t *testing.T) {
	_, settings := tierSetup(t)
	a := claude(filepath.Dir(filepath.Dir(settings)))
	for _, f := range a.Fields[1:] {
		if !f.Override {
			t.Fatalf("tier %s should be an override field", f.Key)
		}
		if len(f.Options(nil)) == 0 {
			t.Fatalf("tier %s offers no models", f.Key)
		}
	}
	if a.Fields[0].Override {
		t.Fatal("main model must not be an override field")
	}
	if err := a.Fields[0].Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if v := tierField(t, a, "opus").Get(); v != "" {
		t.Fatalf("unpinned tier listed as %q", v)
	}
}

func TestTierPiModelEntry(t *testing.T) {
	cases := []struct {
		efforts []string
		want    map[string]string // nil: no thinkingLevelMap at all
	}{
		{[]string{"low", "medium", "high"}, nil},
		{[]string{"none", "low", "medium", "high"}, nil},
		{[]string{"high", "max"}, map[string]string{"max": "max"}},
		{[]string{"low", "xhigh", "high"}, map[string]string{"xhigh": "xhigh"}},
		{[]string{"none", "high", "xhigh", "max"}, map[string]string{"max": "max", "xhigh": "xhigh"}},
	}
	for _, c := range cases {
		e := piModelEntry(catalog.Model{ID: "m", Name: "M", Efforts: c.efforts})
		got, has := e["thinkingLevelMap"]
		if c.want == nil {
			if has {
				t.Fatalf("efforts %v: unexpected thinkingLevelMap %v", c.efforts, got)
			}
			continue
		}
		m, ok := got.(map[string]string)
		if !ok || len(m) != len(c.want) {
			t.Fatalf("efforts %v: thinkingLevelMap = %v, want %v", c.efforts, got, c.want)
		}
		for k, v := range c.want {
			if m[k] != v {
				t.Fatalf("efforts %v: thinkingLevelMap = %v, want %v", c.efforts, m, c.want)
			}
		}
	}
}
