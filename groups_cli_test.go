package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestModelsListsGroupsFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	provider.Save(provider.Provider{ID: "a", Name: "Alpha", Key: "k", Chat: "https://a.example/v1", Models: []string{"m"}})
	provider.Save(provider.Provider{ID: "b", Name: "Beta", Key: "k", Chat: "https://b.example/v1", Models: []string{"vendor/m"}})
	out := modelsListing(provider.Catalog(), 20)
	gi, ai, bi := strings.Index(out, "Routing groups"), strings.Index(out, "Alpha"), strings.Index(out, "Beta")
	if gi < 0 || ai < gi || bi < ai || !strings.Contains(out, "group/auto-m") {
		t.Fatalf("listing:\n%s", out)
	}
	if strings.Index(out, "group/auto-m") > ai {
		t.Fatalf("group not in its section:\n%s", out)
	}
}
