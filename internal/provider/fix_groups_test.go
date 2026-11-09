package provider

import (
	"slices"
	"testing"
)

// FIX: FindGroup takes the id with or without the group/ prefix.
func TestFIXFindGroupAcceptsPrefix(t *testing.T) {
	s2Home(t)
	s2Two(t)
	for _, id := range []string{"auto-m", "group/auto-m"} {
		g, ms, ok := FindGroup(id)
		if !ok || g.ID != "auto-m" || len(ms) != 2 {
			t.Fatalf("FindGroup(%q): %+v members=%v ok=%v", id, g, s2Refs(ms), ok)
		}
	}
}

// FIX: deleting the user's own group does not hide the discovered group that
// shares its id; the discovered one shows again.
func TestFIXDeleteUserGroupKeepsDiscovered(t *testing.T) {
	s2Home(t)
	s2Two(t)
	if err := SaveGroup(Group{ID: "auto-m", Members: []string{"b/vendor/m"}}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteGroup("auto-m"); err != nil {
		t.Fatal(err)
	}
	if h := load().HiddenGroups; len(h) != 0 {
		t.Fatalf("deleting the user's group hid a group: %v", h)
	}
	if _, ms, ok := FindGroup("auto-m"); !ok || !slices.Equal(s2Refs(ms), []string{"a/m", "b/vendor/m"}) {
		t.Fatalf("discovered group after deleting the user's: %v ok=%v", s2Refs(ms), ok)
	}
}
