package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return m
}

func get(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func TestAcceptClaudeTiers(t *testing.T) {
	home := isolate(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash", "mid"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	env := func(k string) any { return get(readJSON(t, path), "env", k) }
	str := func(v any) string { s, _ := v.(string); return s }

	for _, tier := range []string{"opus", "sonnet", "haiku", "fable"} {
		if a.Field(tier) == nil {
			t.Fatalf("no %s field", tier)
		}
	}
	if err := a.Field("haiku").Set("deepseek/flash"); err == nil {
		t.Fatal("a tier before Claude Code is routed through magpie should fail")
	}
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatalf("clearing a tier that was never set: %v", err)
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"OPUS", "SONNET", "HAIKU", "FABLE"} {
		if got := str(env("ANTHROPIC_DEFAULT_" + k + "_MODEL")); got != "deepseek/pro" {
			t.Errorf("%s should follow the model: %q", k, got)
		}
	}
	if str(env("ANTHROPIC_SMALL_FAST_MODEL")) != "deepseek/pro" || str(env("CLAUDE_CODE_SUBAGENT_MODEL")) != "deepseek/pro" {
		t.Errorf("small/fast and subagent should be the model")
	}
	for _, tier := range []string{"opus", "sonnet", "haiku", "fable"} {
		if v := a.Field(tier).Get(); v != "" {
			t.Errorf("%s shows %q while following the model", tier, v)
		}
	}

	if err := a.Field("haiku").Set("deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if str(env("ANTHROPIC_DEFAULT_HAIKU_MODEL")) != "deepseek/flash" || str(env("ANTHROPIC_SMALL_FAST_MODEL")) != "deepseek/flash" {
		t.Errorf("haiku not set: %v", readJSON(t, path))
	}
	if env("CLAUDE_CODE_SUBAGENT_MODEL") != nil && str(env("CLAUDE_CODE_SUBAGENT_MODEL")) != "" {
		t.Errorf("one subagent model should be gone: %v", env("CLAUDE_CODE_SUBAGENT_MODEL"))
	}
	for _, k := range []string{"OPUS", "SONNET", "FABLE"} {
		if got := str(env("ANTHROPIC_DEFAULT_" + k + "_MODEL")); got != "deepseek/pro" {
			t.Errorf("%s: %q", k, got)
		}
	}
	if a.Field("haiku").Get() != "deepseek/flash" || a.Field("opus").Get() != "" {
		t.Errorf("values: %v", a.Values())
	}
	if err := a.Field("sonnet").Set("nope/nothing"); err == nil {
		t.Error("a model magpie does not serve was accepted for a tier")
	}

	// a new main model takes the tiers that followed; haiku stays
	if err := a.Field("model").Set("deepseek/mid"); err != nil {
		t.Fatal(err)
	}
	if str(env("ANTHROPIC_DEFAULT_SONNET_MODEL")) != "deepseek/mid" || str(env("ANTHROPIC_DEFAULT_FABLE_MODEL")) != "deepseek/mid" || str(env("ANTHROPIC_DEFAULT_OPUS_MODEL")) != "deepseek/mid" {
		t.Errorf("followers should move: %v", readJSON(t, path))
	}
	if str(env("ANTHROPIC_DEFAULT_HAIKU_MODEL")) != "deepseek/flash" || str(env("ANTHROPIC_SMALL_FAST_MODEL")) != "deepseek/flash" {
		t.Errorf("haiku should stay: %v", readJSON(t, path))
	}
	if err := a.Field("model").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if str(env("ANTHROPIC_DEFAULT_SONNET_MODEL")) != "deepseek/pro" || str(env("ANTHROPIC_DEFAULT_HAIKU_MODEL")) != "deepseek/flash" {
		t.Errorf("followers move, pinned stay: %v", readJSON(t, path))
	}

	// clearing the tiers brings back one model for everything
	if err := a.Field("haiku").Set(""); err != nil {
		t.Fatal(err)
	}
	if str(env("ANTHROPIC_DEFAULT_HAIKU_MODEL")) != "deepseek/pro" || str(env("ANTHROPIC_DEFAULT_OPUS_MODEL")) != "deepseek/pro" ||
		str(env("ANTHROPIC_SMALL_FAST_MODEL")) != "deepseek/pro" || str(env("CLAUDE_CODE_SUBAGENT_MODEL")) != "deepseek/pro" {
		t.Errorf("not unified again: %v", readJSON(t, path))
	}

	// Claude Code as installed again
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, path)
	if e, _ := m["env"].(map[string]any); len(e) != 0 {
		t.Errorf("reset left env behind: %v", e)
	}
	if m["theme"] != "dark" {
		t.Errorf("theme lost: %v", m)
	}
}

func TestAcceptPiThinkingLevelMap(t *testing.T) {
	home := isolate(t)
	cat := filepath.Join(home, ".cache", "magpie", "models.json")
	if err := os.MkdirAll(filepath.Dir(cat), 0o755); err != nil {
		t.Fatal(err)
	}
	catalogJSON := `{"deepseek":{"id":"deepseek","models":{
	  "deepseek-max":{"id":"deepseek-max","name":"Max","release_date":"2026-01-01","reasoning_options":[{"type":"effort","values":["low","high","max"]}]},
	  "deepseek-xh":{"id":"deepseek-xh","name":"XH","release_date":"2026-01-02","reasoning_options":[{"type":"effort","values":["none","low","high","xhigh","max"]}]},
	  "deepseek-std":{"id":"deepseek-std","name":"Std","release_date":"2026-01-03","reasoning_options":[{"type":"effort","values":["low","medium","high"]}]},
	  "deepseek-plain":{"id":"deepseek-plain","name":"Plain","release_date":"2026-01-04"}
	}}}`
	if err := os.WriteFile(cat, []byte(catalogJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "ds", Name: "DS", Chat: "https://api.deepseek.com/v1", Key: "k", Catalog: "deepseek",
		Models: []string{"deepseek-max", "deepseek-xh", "deepseek-std", "deepseek-plain"}}); err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find("pi")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("magpie/ds/deepseek-max"); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(home, ".pi", "agent", "models.json")
	check := func() {
		t.Helper()
		ms, _ := get(readJSON(t, modelsPath), "providers", "magpie", "models").([]any)
		by := map[string]map[string]any{}
		for _, m := range ms {
			mm := m.(map[string]any)
			by[mm["id"].(string)] = mm
		}
		if len(by) != 4 {
			t.Fatalf("models: %v", ms)
		}
		lv := func(id string) map[string]any { m, _ := by[id]["thinkingLevelMap"].(map[string]any); return m }
		if l := lv("ds/deepseek-max"); len(l) != 1 || l["max"] != "max" {
			t.Errorf("max model: %v", by["ds/deepseek-max"])
		}
		if l := lv("ds/deepseek-xh"); len(l) != 2 || l["max"] != "max" || l["xhigh"] != "xhigh" {
			t.Errorf("xhigh model: %v", by["ds/deepseek-xh"])
		}
		for _, id := range []string{"ds/deepseek-std", "ds/deepseek-plain"} {
			if l := lv(id); len(l) != 0 {
				t.Errorf("%s should declare nothing: %v", id, l)
			}
		}
	}
	check()

	// an older file without the declarations is rewritten when the thinking level is chosen
	os.Remove(modelsPath)
	if err := a.Field("effort").Set("max"); err != nil {
		t.Fatal(err)
	}
	check()
}
