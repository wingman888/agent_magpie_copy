package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The routing groups get their own section, ahead of every provider's.
func TestF09PrintModelsGroupsBeforeProviders(t *testing.T) {
	a := provider.Provider{ID: "a", Name: "A"}
	entries := []provider.Entry{
		{ID: "a/m", Model: "m", Name: "m", Provider: a},
		{ID: "group/auto-m", Name: "Claude Opus 5.5", Efforts: []string{"low", "high"}, Group: "auto-m"},
		{ID: "a/only-a", Model: "only-a", Name: "only-a", Provider: a},
	}
	var buf bytes.Buffer
	printModels(&buf, entries)
	out := buf.String()
	g := strings.Index(out, "Routing groups")
	p := strings.Index(out, "  A")
	if g < 0 || p < 0 || g > p {
		t.Fatalf("Routing groups must come before provider A:\n%s", out)
	}
	if !strings.Contains(out[g:p], "group/auto-m") || !strings.Contains(out[g:p], "Claude Opus 5.5") {
		t.Fatalf("group line missing from its section:\n%s", out)
	}
	if !strings.Contains(out[g:p], "low/high") {
		t.Fatalf("group efforts missing:\n%s", out)
	}
	// a group is never listed under a provider, even with no provider to name
	if strings.Count(out, "Routing groups") != 1 || strings.Contains(out[p:], "group/auto-m") {
		t.Fatalf("group under a provider:\n%s", out)
	}
}

// With no groups there is no section header at all.
func TestF09PrintModelsNoGroups(t *testing.T) {
	var buf bytes.Buffer
	printModels(&buf, []provider.Entry{{ID: "a/m", Model: "m", Name: "m", Provider: provider.Provider{ID: "a", Name: "A"}}})
	if strings.Contains(buf.String(), "Routing groups") {
		t.Fatalf("unexpected section:\n%s", buf.String())
	}
}
