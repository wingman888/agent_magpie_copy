package agent

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A picker lists the routing groups first, in their own section, noted as
// routed by magpie, with each member provider's icon once.
func TestXRPickerRoutingGroupsFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "pa", Name: "PA", Chat: "https://a.example.com/v1", Key: "k", Models: []string{"m"}, Icon: "icon-a"},
		{ID: "pb", Name: "PB", Chat: "https://b.example.com/v1", Key: "k", Models: []string{"m", "n"}, Icon: "icon-b"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"pa/m", "pb/m", "pa/m"}}); err != nil {
		t.Fatal(err)
	}

	opts := viaMagpie("")
	if len(opts) == 0 {
		t.Fatal("no options")
	}
	var mine *Option
	for i := range opts {
		if opts[i].Value == "group/mine" {
			mine = &opts[i]
		}
	}
	if mine == nil {
		t.Fatalf("group not listed: %+v", opts)
	}
	if opts[0].Value != "group/mine" {
		t.Fatalf("group not first: %q", opts[0].Value)
	}
	if mine.Group != "Routing groups" {
		t.Errorf("section %q, want Routing groups", mine.Group)
	}
	if mine.Note != "routing group · via magpie" {
		t.Errorf("note %q", mine.Note)
	}
	if want := []string{"icon-a", "icon-b"}; !slices.Equal(mine.Icons, want) {
		t.Errorf("icons %v, want %v", mine.Icons, want)
	}
	for _, o := range opts[1:] {
		if o.Group == "Routing groups" {
			t.Errorf("a non-group option %q is in the routing groups section", o.Value)
		}
	}
}
