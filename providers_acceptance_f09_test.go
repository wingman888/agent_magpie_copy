package main

// Acceptance item 1 at the command line: `magpie models` lists the routing
// groups in a section of their own, before the providers.

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

func TestAcceptance01CLIModelsRoutingGroupsFirst_f09(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	// A offers m, B offers vendor/m: the same model, so group/auto-m exists
	save := func(id, name, chat string, models ...catalog.Model) {
		t.Helper()
		if err := provider.Save(provider.Provider{ID: id, Name: name, Key: "k-" + id, Chat: chat}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive(id, chat, models); err != nil {
			t.Fatal(err)
		}
	}
	save("a", "Alpha", "https://a.example/v1", catalog.Model{ID: "m", Name: "M"}, catalog.Model{ID: "only-a"})
	save("b", "Beta", "https://b.example/v1", catalog.Model{ID: "vendor/m", Name: "Vendor M"})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := models()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	r.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}

	s := string(out)
	gi := strings.Index(s, "Routing groups")
	ai := strings.Index(s, "Alpha")
	bi := strings.Index(s, "Beta")
	if gi < 0 {
		t.Fatalf("no Routing groups section:\n%s", s)
	}
	if ai < 0 || bi < 0 || gi > ai || gi > bi {
		t.Fatalf("Routing groups must come before the providers:\n%s", s)
	}
	if gl := strings.Index(s, "group/auto-m"); gl < gi || gl > ai {
		t.Fatalf("group/auto-m must sit in the Routing groups section, before the providers:\n%s", s)
	}
}
