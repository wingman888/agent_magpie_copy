package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// `magpie claude <model>` sets the main model even when a tier offers the
// same name, and `magpie claude <tier> <model>` pins only that tier.
func TestClaudeCLIBareModelIsMainModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"claude", "deepseek/pro"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"claude", "haiku", "deepseek/flash"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"claude", "deepseek/flash"}); err != nil {
		t.Fatal(err)
	}

	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	v := a.Values()
	if v["model"] != "deepseek/flash" {
		t.Fatalf("main model = %q, want deepseek/flash", v["model"])
	}
	if v["haiku"] != "deepseek/flash" {
		t.Fatalf("haiku pin = %q, want it kept", v["haiku"])
	}
	if v["opus"] != "" {
		t.Fatalf("opus = %q, want it to follow (unset)", v["opus"])
	}

	// clearing a tier with an empty argument restores the unified subagent model
	if err := run([]string{"claude", "haiku", ""}); err != nil {
		t.Fatal(err)
	}
	if a.Values()["haiku"] != "" {
		t.Fatalf("haiku still pinned")
	}
	if err := run([]string{"claude", "default"}); err != nil {
		t.Fatal(err)
	}
	if a.Values()["model"] != "" {
		t.Fatalf("main model not cleared")
	}
}
