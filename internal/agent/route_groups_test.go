package agent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The picker lists routing groups first, in a section of their own, each
// with the icons of its members' providers once.
func TestPickerRoutingGroupsFirst(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "only-a"}, Icon: "ia"},
		{ID: "b", Name: "B", Key: "k", Chat: "http://127.0.0.1:2/v1", Models: []string{"m"}, Icon: "ib"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "mine", Name: "Mine", Members: []string{"a/m", "b/m"}}); err != nil {
		t.Fatal(err)
	}

	opts := viaMagpie("magpie/")
	var groups []Option
	for i, o := range opts {
		if o.Group == "Routing groups" {
			if i != len(groups) {
				t.Fatalf("routing group %q at %d, not at the top", o.Value, i)
			}
			groups = append(groups, o)
		}
	}
	if len(groups) == 0 {
		t.Fatalf("no routing groups in the picker: %+v", opts)
	}
	var mine *Option
	for i := range groups {
		if groups[i].Value == "magpie/group/mine" {
			mine = &groups[i]
		}
	}
	if mine == nil {
		t.Fatalf("group mine missing: %+v", groups)
	}
	if mine.Label != "Mine" || mine.Note != "routing group · via magpie" {
		t.Errorf("group option %+v", *mine)
	}
	if !slices.Equal(mine.Icons, []string{"ia", "ib"}) {
		t.Errorf("icons %v, want one per provider", mine.Icons)
	}
	for _, o := range opts[len(groups):] {
		if strings.HasPrefix(o.Value, "magpie/group/") {
			t.Errorf("group %q listed after providers", o.Value)
		}
	}
}
