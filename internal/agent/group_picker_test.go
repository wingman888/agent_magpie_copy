package agent

import (
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Two ready providers offering the same model, sharing one logo, plus one
// provider of its own, so the catalog holds a discovered group.
func pickerSetup(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "a", Name: "Alpha", Chat: "https://a.test/v1", Key: "sk-a-1111", Models: []string{"m"}, Icon: "openai"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "Beta", Chat: "https://b.test/v1", Key: "sk-b-2222", Models: []string{"vendor/m"}, Icon: "openai"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "c", Name: "Gamma", Chat: "https://c.test/v1", Key: "sk-c-3333", Models: []string{"other"}, Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
}

// The picker lists the routing group first, in a section of its own, with
// the note and one icon per member provider.
func TestViaMagpieRoutingGroupsFirst(t *testing.T) {
	pickerSetup(t)
	opts := viaMagpie("magpie/")
	if len(opts) == 0 {
		t.Fatal("no options")
	}
	g := opts[0]
	if g.Value != "magpie/group/auto-m" || g.Group != "Routing groups" || g.Note != "routing group · via magpie" || g.Ref != "group/auto-m" {
		t.Fatalf("first option %+v", g)
	}
	if !slices.Equal(g.Icons, []string{"openai"}) {
		t.Fatalf("icons %v, want one openai", g.Icons)
	}
	if g.Icon != "" {
		t.Fatalf("group Icon %q, want empty", g.Icon)
	}
	for _, o := range opts[1:] {
		if o.Group == "Routing groups" {
			t.Fatalf("group section continues after a provider: %+v", o)
		}
	}
	if len(opts) != 4 {
		t.Fatalf("%d options, want 4 (group + three provider models)", len(opts))
	}
}
