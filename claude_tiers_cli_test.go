package main

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// A bare provider/model value is the main model, never one of the tiers, even
// though the tiers offer the same models.
func TestBareModelIsTheClaudeMainModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1",
		Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	if f := fieldForValue(a, "deepseek/pro"); f != nil {
		t.Fatalf("bare model routed to the %s field", f.Key)
	}
}
