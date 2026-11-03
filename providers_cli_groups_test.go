package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The catalog listing gives the routing groups a section of their own, ahead
// of every provider, and a group line names the models it draws on rather
// than a blank provider.
func TestF09CatalogListsRoutingGroupsFirst(t *testing.T) {
	group := &provider.Group{ID: "mine", Name: "Mine", Members: []provider.Member{
		{Provider: "b", Model: "vendor/m"}, {Provider: "a", Model: "only-a"}}}
	entries := []provider.Entry{
		{ID: "alpha/m", Model: "m", Name: "M", Provider: provider.Provider{ID: "alpha", Name: "Alpha"}},
		{ID: "group/mine", Name: "Mine", Group: group},
		{ID: "beta/vendor/m", Model: "vendor/m", Name: "Vendor M", Provider: provider.Provider{ID: "beta", Name: "Beta"}},
	}
	lines := catalogLines(entries)
	index := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		return -1
	}
	heading, alpha, groupLine := index("Routing groups"), index("Alpha"), index("group/mine")
	if heading != 0 {
		t.Fatalf("Routing groups heading at %d, want first: %q", heading, lines)
	}
	if groupLine <= heading || groupLine >= alpha {
		t.Fatalf("group line at %d, want between the heading (%d) and Alpha (%d): %q", groupLine, heading, alpha, lines)
	}
	if !strings.Contains(lines[groupLine], "Mine") || !strings.Contains(lines[groupLine], "b/vendor/m") || !strings.Contains(lines[groupLine], "a/only-a") {
		t.Fatalf("group line does not name its members: %q", lines[groupLine])
	}
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			t.Fatalf("blank line in listing: %q", lines)
		}
	}
	if index("Beta") <= alpha {
		t.Fatalf("providers out of order: %q", lines)
	}
}

// With no routing groups there is no heading to print.
func TestF09CatalogWithoutGroupsHasNoHeading(t *testing.T) {
	lines := catalogLines([]provider.Entry{{ID: "a/m", Model: "m", Name: "M", Provider: provider.Provider{ID: "a", Name: "A"}}})
	for _, l := range lines {
		if strings.Contains(l, "Routing groups") {
			t.Fatalf("heading without groups: %q", lines)
		}
	}
}

// An agent routed to a routing group is not counted under a provider.
func TestF09ProviderOfModelSkipsGroups(t *testing.T) {
	for v, want := range map[string]struct {
		pid string
		ok  bool
	}{
		"group/mine":        {"", false},
		"magpie/group/mine": {"", false},
		"anthropic/claude":  {"anthropic", true},
		"magpie/relay/gpt":  {"relay", true},
		"claude-opus":       {"", false},
	} {
		pid, ok := providerOfModel(v)
		if pid != want.pid || ok != want.ok {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", v, pid, ok, want.pid, want.ok)
		}
	}
}
