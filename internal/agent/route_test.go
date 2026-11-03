package agent

import (
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// groupEntryFor is a routing group's catalog entry over the given members.
func groupEntryFor(id, name string, ms ...provider.Member) provider.Entry {
	return provider.Entry{ID: "group/" + id, Name: name, Group: &provider.Group{ID: id, Name: name}, Members: ms}
}

func TestPickerGroupsFirstWithIcons(t *testing.T) {
	a := provider.Provider{ID: "a", Name: "A", Icon: "icon-a"}
	b := provider.Provider{ID: "b", Name: "B", Icon: "icon-b"}
	c := provider.Provider{ID: "c", Name: "C", Icon: "icon-b"} // same icon as b
	entries := []provider.Entry{
		{ID: "a/m", Model: "m", Name: "M", Provider: a},
		{ID: "b/m", Model: "m", Name: "M", Provider: b},
		groupEntryFor("mine", "Mine",
			provider.Member{Provider: "b", Model: "m", Info: b},
			provider.Member{Provider: "b", Model: "n", Info: b}, // second model of b: b's icon once
			provider.Member{Provider: "c", Model: "m", Info: c},
			provider.Member{Provider: "a", Model: "m", Info: a}),
	}
	got := magpieOptions(entries, "magpie/")
	if len(got) != 3 {
		t.Fatalf("want 3 options, got %d: %+v", len(got), got)
	}
	g := got[0]
	if g.Group != "Routing groups" || g.Value != "magpie/group/mine" || g.Label != "Mine" {
		t.Fatalf("group option not first or misnamed: %+v", g)
	}
	if !strings.Contains(g.Note, "routing group · via magpie") {
		t.Fatalf("group note: %q", g.Note)
	}
	if want := []string{"icon-b", "icon-a"}; !slices.Equal(g.Icons, want) {
		t.Fatalf("group icons: got %v want %v", g.Icons, want)
	}
	if g.Icon != "icon-b" {
		t.Fatalf("group icon: got %q", g.Icon)
	}
	for _, o := range got[1:] {
		if o.Group == "Routing groups" {
			t.Fatalf("provider option in the routing groups section: %+v", o)
		}
	}
	if got[1].Group != "A" || got[2].Group != "B" {
		t.Fatalf("provider sections: %q, %q", got[1].Group, got[2].Group)
	}
}

func TestPickerGroupWithoutIcons(t *testing.T) {
	entries := []provider.Entry{groupEntryFor("bare", "Bare", provider.Member{Provider: "x", Model: "m"})}
	got := magpieOptions(entries, "")
	if len(got) != 1 || got[0].Icon != "" || len(got[0].Icons) != 0 {
		t.Fatalf("no icons expected: %+v", got)
	}
}

func TestIsMagpieGroup(t *testing.T) {
	if !isMagpie("group/mine") || !isMagpie("group/auto-m") {
		t.Fatal("a routing group is a magpie model")
	}
}
