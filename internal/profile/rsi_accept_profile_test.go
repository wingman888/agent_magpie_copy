package profile_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/profile"
	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptProfileAppliesTiersAfterModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.Apply(profile.Profile{
		"claude.haiku": "deepseek/flash",
		"claude.model": "deepseek/pro",
		"claude.fable": "deepseek/flash",
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	var m struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	e := m.Env
	if e["ANTHROPIC_MODEL"] != "deepseek/pro" || e["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "deepseek/flash" ||
		e["ANTHROPIC_DEFAULT_FABLE_MODEL"] != "deepseek/flash" || e["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "deepseek/pro" {
		t.Fatalf("env: %v", e)
	}
}
