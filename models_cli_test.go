package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The routing groups section comes first in `magpie models`, under its own
// heading, and each group shows its name and its group/<id>.
func TestMcliListModelsGroupsFirst(t *testing.T) {
	a := provider.Provider{ID: "a", Name: "Alpha"}
	entries := []provider.Entry{
		{ID: "a/m", Model: "m", Name: "m", Provider: a},
		{ID: "group/mine", Name: "Mine", Group: &provider.Group{ID: "mine", Name: "Mine"}},
	}
	var buf bytes.Buffer
	listModels(&buf, entries)
	out := buf.String()

	head := strings.Index(out, "Routing groups")
	prov := strings.Index(out, "Alpha")
	if head < 0 || prov < 0 {
		t.Fatalf("missing section headings:\n%s", out)
	}
	if head > prov {
		t.Fatalf("routing groups must come before the providers:\n%s", out)
	}
	if !strings.Contains(out[head:prov], "group/mine") || !strings.Contains(out[head:prov], "Mine") {
		t.Fatalf("group line not in its section:\n%s", out)
	}
	if !strings.Contains(out, "a/m") {
		t.Fatalf("provider model missing:\n%s", out)
	}
}

// Without groups there is no routing groups heading at all.
func TestMcliListModelsNoGroups(t *testing.T) {
	entries := []provider.Entry{{ID: "a/m", Model: "m", Name: "m", Provider: provider.Provider{ID: "a", Name: "Alpha"}}}
	var buf bytes.Buffer
	listModels(&buf, entries)
	if strings.Contains(buf.String(), "Routing groups") {
		t.Fatalf("unexpected routing groups section:\n%s", buf.String())
	}
}
