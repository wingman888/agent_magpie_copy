package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The built-in icon list must match the icons the GUI embeds.
func TestBuiltinIconsMatchGUIAssets(t *testing.T) {
	ents, err := os.ReadDir(filepath.Join("..", "gui", "assets", "icons"))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		files[strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))] = true
	}
	for name := range files {
		if !IsBuiltinIcon(name) {
			t.Errorf("GUI icon %q is missing from builtinIcons", name)
		}
	}
	for name := range rsiBuiltinIcons {
		if !files[name] {
			t.Errorf("builtinIcons lists %q, which the GUI does not ship", name)
		}
	}
}

func TestIsBuiltinIcon(t *testing.T) {
	if !IsBuiltinIcon("openai") || IsBuiltinIcon("no-such-icon") || IsBuiltinIcon("") {
		t.Fatal("IsBuiltinIcon gives the wrong answer")
	}
}
