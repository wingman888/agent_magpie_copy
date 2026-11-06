package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The models listing puts the routing groups in a section of their own,
// ahead of every provider; with no group the listing has no such section.
func TestListModelsGroupsSectionFirst(t *testing.T) {
	a := provider.Provider{ID: "a", Name: "Alpha"}
	b := provider.Provider{ID: "b", Name: "Beta"}
	entries := []provider.Entry{
		{ID: "a/m", Model: "m", Name: "M", Provider: a},
		{ID: "b/vendor/m", Model: "vendor/m", Name: "Vendor M", Provider: b},
		{ID: "group/auto-m", Name: "M", Kind: provider.KindGroup, Members: []string{"a/m", "b/vendor/m"}, Auto: true},
		{ID: "group/mine", Name: "Mine", Kind: provider.KindGroup, Members: []string{"b/vendor/m", "a/m"}},
	}
	var buf bytes.Buffer
	if err := listModels(&buf, entries); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	sec := strings.Index(out, "Routing groups")
	if sec < 0 {
		t.Fatalf("no Routing groups section:\n%s", out)
	}
	for _, p := range []string{"Alpha", "Beta"} {
		if i := strings.Index(out, p); i < 0 || i < sec {
			t.Fatalf("provider %s should come after the groups section:\n%s", p, out)
		}
	}
	if !strings.Contains(out[sec:], "group/mine") || !strings.Contains(out[sec:], "Mine") {
		t.Fatalf("group row missing:\n%s", out)
	}
	if strings.Contains(out, "  \n") || strings.Contains(out, "\x1b[0m  \n") {
		t.Fatalf("a group was listed under an empty provider name:\n%q", out)
	}
}

func TestListModelsWithoutGroupsHasNoSection(t *testing.T) {
	entries := []provider.Entry{{ID: "a/m", Model: "m", Name: "m", Provider: provider.Provider{ID: "a", Name: "Alpha"}}}
	var buf bytes.Buffer
	listModels(&buf, entries)
	if strings.Contains(buf.String(), "Routing groups") {
		t.Fatalf("unexpected section:\n%s", buf.String())
	}
}
