package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// claudeHome sets up a home with one ready provider and a Claude Code
// settings file; routed says whether Claude already points at the gateway.
func claudeHome(t *testing.T, routed bool) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "k", Models: []string{"pro", "flash", "chat"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"theme": "dark"}`
	if routed {
		body = `{"env": {"ANTHROPIC_BASE_URL": "` + gateway.URL() + `", "ANTHROPIC_AUTH_TOKEN": "magpie", "ANTHROPIC_MODEL": "deepseek/pro"}, "model": "deepseek/pro", "theme": "dark"}`
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	return a, path
}

func env(t *testing.T, path, key string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, "env."+key)
	return v
}

func setField(t *testing.T, a *Agent, key, v string) error {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	return f.Set(v)
}

func TestClaudeTiersFollowMainModel(t *testing.T) {
	a, path := claudeHome(t, false)
	if err := setField(t, a, "model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "ANTHROPIC_MODEL"} {
		if got := env(t, path, k); got != "deepseek/pro" {
			t.Errorf("%s = %q, want deepseek/pro", k, got)
		}
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Errorf("theme lost: %q", v)
	}
	if !a.Field("haiku").Optional {
		t.Error("tier fields should be optional in listings")
	}
	if got := a.Field("haiku").Get(); got != "" {
		t.Errorf("unpinned tier shows %q, want empty", got)
	}
}

func TestClaudeHaikuPinCancelsUnifiedSubagent(t *testing.T) {
	a, path := claudeHome(t, false)
	if err := setField(t, a, "model", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := setField(t, a, "haiku", "deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if got := env(t, path, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/flash" {
		t.Errorf("haiku = %q", got)
	}
	if got := env(t, path, "ANTHROPIC_SMALL_FAST_MODEL"); got != "deepseek/flash" {
		t.Errorf("small fast = %q, want haiku's model", got)
	}
	for _, k := range []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_MODEL"} {
		if got := env(t, path, k); got != "deepseek/pro" {
			t.Errorf("%s = %q, want deepseek/pro", k, got)
		}
	}
	if got := env(t, path, "CLAUDE_CODE_SUBAGENT_MODEL"); got != "" {
		t.Errorf("subagent model still unified: %q", got)
	}
	if got := a.Field("haiku").Get(); got != "deepseek/flash" {
		t.Errorf("haiku Get = %q", got)
	}
}

func TestClaudeChangingMainKeepsPinnedTier(t *testing.T) {
	a, path := claudeHome(t, false)
	setField(t, a, "model", "deepseek/pro")
	setField(t, a, "haiku", "deepseek/flash")
	if err := setField(t, a, "model", "deepseek/chat"); err != nil {
		t.Fatal(err)
	}
	if got := env(t, path, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/flash" {
		t.Errorf("pinned haiku changed to %q", got)
	}
	if got := env(t, path, "ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "deepseek/chat" {
		t.Errorf("following opus = %q, want new main", got)
	}
	if got := env(t, path, "ANTHROPIC_SMALL_FAST_MODEL"); got != "deepseek/flash" {
		t.Errorf("small fast = %q, want pinned haiku", got)
	}
}

func TestClaudeClearHaikuRestoresUnified(t *testing.T) {
	a, path := claudeHome(t, false)
	setField(t, a, "model", "deepseek/pro")
	setField(t, a, "haiku", "deepseek/flash")
	if err := setField(t, a, "haiku", ""); err != nil {
		t.Fatal(err)
	}
	if got := env(t, path, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/pro" {
		t.Errorf("haiku = %q, want main", got)
	}
	if got := env(t, path, "CLAUDE_CODE_SUBAGENT_MODEL"); got != "deepseek/pro" {
		t.Errorf("subagent model = %q, want unified main", got)
	}
	if got := a.Field("haiku").Get(); got != "" {
		t.Errorf("haiku still pinned: %q", got)
	}
}

func TestClaudeTierNeedsMagpieMainModel(t *testing.T) {
	a, path := claudeHome(t, false)
	if err := setField(t, a, "haiku", "deepseek/flash"); err == nil || !strings.Contains(err.Error(), "magpie") {
		t.Fatalf("tier before routing: err = %v, want a hint to pick a magpie model", err)
	}
	if err := setField(t, a, "haiku", ""); err != nil {
		t.Fatalf("clearing a tier without routing should do nothing, got %v", err)
	}
	if got := env(t, path, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "" {
		t.Errorf("unrouted settings were touched: %q", got)
	}
}

func TestClaudeTierRejectsNonMagpieModel(t *testing.T) {
	a, _ := claudeHome(t, false)
	setField(t, a, "model", "deepseek/pro")
	if err := setField(t, a, "opus", "claude-opus-4"); err == nil {
		t.Fatal("a model magpie does not provide was accepted for a tier")
	}
}

func TestClaudeClearMainRemovesTiersKeepsTheme(t *testing.T) {
	a, path := claudeHome(t, false)
	setField(t, a, "model", "deepseek/pro")
	setField(t, a, "haiku", "deepseek/flash")
	if err := setField(t, a, "model", ""); err != nil {
		t.Fatal(err)
	}
	for _, k := range claudeEnv {
		if got := env(t, path, k); got != "" {
			t.Errorf("%s survived clearing the main model: %q", k, got)
		}
	}
	if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
		t.Errorf("theme lost: %q", v)
	}
	if got := a.Field("haiku").Get(); got != "" {
		t.Errorf("pin survived clearing the main model: %q", got)
	}
}

func TestClaudeUsesFableVariable(t *testing.T) {
	if !slices.Contains(claudeEnv, "ANTHROPIC_DEFAULT_FABLE_MODEL") {
		t.Fatal("fable tier variable is not managed")
	}
}

func TestPiThinkingLevelMap(t *testing.T) {
	ms := piModels([]catalog.Model{
		{ID: "glm", Name: "GLM", Efforts: []string{"low", "high", "max"}},
		{ID: "plain", Name: "Plain", Efforts: []string{"low", "medium", "high"}},
		{ID: "gpt", Name: "GPT", Efforts: []string{"high", "xhigh"}},
		{ID: "none", Name: "None"},
	})
	if len(ms) != 4 {
		t.Fatalf("got %d models", len(ms))
	}
	tl := func(i int) map[string]string {
		m, _ := ms[i]["thinkingLevelMap"].(map[string]string)
		return m
	}
	if got := tl(0); len(got) != 1 || got["max"] != "max" {
		t.Errorf("glm thinkingLevelMap = %v, want only max:max", got)
	}
	if tl(1) != nil {
		t.Errorf("plain model should declare nothing, got %v", tl(1))
	}
	if got := tl(2); len(got) != 1 || got["xhigh"] != "xhigh" {
		t.Errorf("gpt thinkingLevelMap = %v, want only xhigh:xhigh", got)
	}
	if _, ok := ms[3]["thinkingLevelMap"]; ok {
		t.Error("model with no efforts should have no thinkingLevelMap")
	}
	if ms[0]["reasoning"] != true {
		t.Error("reasoning should stay on for models with efforts")
	}
}
