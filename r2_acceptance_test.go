package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// R2 acceptance 1 (CLI part): the model list puts the discovered routing
// group in its own "Routing groups" section, ahead of the providers.
func TestR2ModelsListGroupSectionFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "a", Name: "Alpha", Key: "ka", Chat: "https://a.example/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "Beta", Key: "kb", Chat: "https://b.example/v1", Models: []string{"vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printModels(&out, provider.Catalog())
	s := out.String()

	sec := strings.Index(s, "Routing groups")
	grp := strings.Index(s, "group/auto-m")
	alpha := strings.Index(s, "Alpha")
	beta := strings.Index(s, "Beta")
	if sec < 0 || grp < 0 {
		t.Fatalf("no Routing groups section with group/auto-m:\n%s", s)
	}
	if !(sec < grp && grp < alpha && grp < beta) {
		t.Fatalf("group must be in its section before the providers:\n%s", s)
	}
}
