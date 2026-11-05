package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// rgcModelsEnv isolates the config and cache for a test of the catalog.
func rgcModelsEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// rgcAddKeyProvider saves a keyed provider named name whose list is models.
func rgcAddKeyProvider(t *testing.T, id, name string, models ...catalog.Model) {
	t.Helper()
	base := "https://" + id + ".example/v1"
	if err := provider.Save(provider.Provider{ID: id, Name: name, Chat: base, Key: "sk-" + id}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive(id, base, models); err != nil {
		t.Fatal(err)
	}
}

func rgcRenderModels(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	if err := writeModels(&buf, provider.Catalog()); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestRgModelsRoutingGroupsComeFirst(t *testing.T) {
	rgcModelsEnv(t)
	rgcAddKeyProvider(t, "a", "Alpha", catalog.Model{ID: "m", Name: "m"})
	rgcAddKeyProvider(t, "b", "Bravo", catalog.Model{ID: "vendor/m", Name: "Vendor: M"})

	out := rgcRenderModels(t)
	g := strings.Index(out, "Routing groups")
	if g < 0 {
		t.Fatalf("no Routing groups section:\n%s", out)
	}
	if !strings.Contains(out, "group/auto-m") {
		t.Fatalf("auto group missing:\n%s", out)
	}
	for _, name := range []string{"Alpha", "Bravo"} {
		if p := strings.Index(out, "  "+name); p < 0 || p < g {
			t.Fatalf("provider %s should come after Routing groups:\n%s", name, out)
		}
	}
}

func TestRgModelsWithoutGroupsHasNoSection(t *testing.T) {
	rgcModelsEnv(t)
	if err := provider.Save(provider.Provider{ID: "a", Name: "Alpha", Chat: "https://a.example/v1", Key: "sk-a"}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("a", "https://a.example/v1", []catalog.Model{{ID: "only-a", Name: "only-a"}}); err != nil {
		t.Fatal(err)
	}

	out := rgcRenderModels(t)
	if strings.Contains(out, "Routing groups") || strings.Contains(out, "group/") {
		t.Fatalf("a provider alone should list no groups:\n%s", out)
	}
	if !strings.HasPrefix(out, "  Alpha\n") {
		t.Fatalf("provider listing changed:\n%s", out)
	}
}

func TestRgEmptyCatalogMessage(t *testing.T) {
	var buf bytes.Buffer
	if err := writeModels(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "no models yet") {
		t.Fatalf("%q", buf.String())
	}
}

func TestRgProviderOfSkipsRoutingGroups(t *testing.T) {
	for v, want := range map[string]struct {
		pid string
		ok  bool
	}{
		"magpie/group/mine": {"", false},
		"group/mine":        {"", false},
		"magpie/a/m":        {"a", true},
		"b/vendor/m":        {"b", true},
		"bare-model":        {"", false},
	} {
		pid, ok := providerOf(v)
		if pid != want.pid || ok != want.ok {
			t.Errorf("providerOf(%q) = %q, %v; want %q, %v", v, pid, ok, want.pid, want.ok)
		}
	}
}
