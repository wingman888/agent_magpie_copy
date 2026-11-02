package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Routing groups lead the picker, in a section of their own.
func TestGrpPickerListsGroupsFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	provider.Save(provider.Provider{ID: "a", Key: "k", Icon: "openai", Chat: "https://a.example/v1", Models: []string{"m"}})
	provider.Save(provider.Provider{ID: "b", Key: "k", Icon: "openai", Chat: "https://b.example/v1", Models: []string{"vendor/m"}})
	provider.Save(provider.Provider{ID: "c", Key: "k", Icon: "claude", Chat: "https://c.example/v1", Models: []string{"M"}})
	opts := viaMagpie("magpie/")
	if len(opts) != 4 {
		t.Fatalf("options %+v", opts)
	}
	g := opts[0]
	if g.Value != "magpie/group/auto-m" || g.Group != "Routing groups" || g.Note != "routing group · via magpie" ||
		len(g.Icons) != 2 || g.Icons[0] != "openai" || g.Icons[1] != "claude" || g.Ref != "group/auto-m" {
		t.Fatalf("group option %+v", g)
	}
	if !isMagpie("group/auto-m") {
		t.Fatal("group is a catalog reference")
	}
}
