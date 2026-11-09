package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestS4ModelListingGroupsFirst(t *testing.T) {
	grp := &provider.Group{ID: "mine"}
	entries := []provider.Entry{
		{ID: "a/m", Model: "m", Name: "M", Provider: provider.Provider{ID: "a", Name: "Alpha"}},
		{ID: "group/mine", Model: "group/mine", Name: "My Group", Group: grp},
		{ID: "b/vendor/m", Model: "vendor/m", Name: "M", Provider: provider.Provider{ID: "b", Name: "Beta"}},
	}
	lines := modelListing(entries)
	at := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		return -1
	}
	sec, grpLine, alpha, beta := at("Routing groups"), at("My Group"), at("Alpha"), at("Beta")
	if sec < 0 || grpLine < 0 || alpha < 0 || beta < 0 {
		t.Fatalf("missing lines: %q", lines)
	}
	if !(sec < grpLine && grpLine < alpha && alpha < beta) {
		t.Fatalf("order wrong: %q", lines)
	}
	if !strings.Contains(lines[grpLine], "group/mine") {
		t.Fatalf("group line should carry its id: %q", lines[grpLine])
	}
}

func TestS4ModelListingNoGroups(t *testing.T) {
	lines := modelListing([]provider.Entry{{ID: "a/m", Model: "m", Name: "m", Provider: provider.Provider{ID: "a", Name: "Alpha"}}})
	for _, l := range lines {
		if strings.Contains(l, "Routing groups") {
			t.Fatalf("no section without groups: %q", lines)
		}
	}
}
