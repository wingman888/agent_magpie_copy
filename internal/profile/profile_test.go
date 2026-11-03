package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// A profile that pins a Claude Code tier and sets the main model must make
// the model first: the tier can only be set on top of a magpie main model.
func TestApplySetsModelBeforeTiers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// run it a few times: map order must not matter
	for i := 0; i < 20; i++ {
		p := Profile{
			"claude.haiku": "deepseek/flash",
			"claude.model": "deepseek/pro",
		}
		if _, err := Apply(p); err != nil {
			t.Fatalf("apply %d: %v", i, err)
		}
		if v, _ := edit.GetJSON(settings, "env.ANTHROPIC_DEFAULT_HAIKU_MODEL"); v != "deepseek/flash" {
			t.Fatalf("haiku = %q", v)
		}
		if v, _ := edit.GetJSON(settings, "env.ANTHROPIC_MODEL"); v != "deepseek/pro" {
			t.Fatalf("model = %q", v)
		}
	}
}
