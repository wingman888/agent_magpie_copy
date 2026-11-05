package main

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// `magpie models`: the Routing groups section comes before every provider.
func TestModelLinesRoutingGroupsFirst(t *testing.T) {
	alpha := provider.Provider{ID: "a", Name: "Alpha"}
	group := provider.Provider{ID: "group", Name: "Routing groups"}
	cat := []provider.Entry{
		{ID: "a/m", Model: "m", Name: "M", Provider: alpha},
		{ID: "group/mine", Model: "group/mine", Name: "Mine", Group: "mine", Provider: group},
	}
	lines := modelLines(cat)
	at := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("no line with %q in %q", sub, lines)
		return -1
	}
	groups, providerHead, groupRow, modelRow := at("Routing groups"), at("Alpha"), at("group/mine"), at("a/m")
	if !(groups < groupRow && groupRow < providerHead && providerHead < modelRow) {
		t.Fatalf("order wrong, got %q", lines)
	}
	if !strings.Contains(lines[groupRow], "Mine") {
		t.Fatalf("group row lacks its name: %q", lines[groupRow])
	}
}

// Without groups there is no Routing groups section at all.
func TestModelLinesNoGroups(t *testing.T) {
	cat := []provider.Entry{{ID: "a/m", Model: "m", Name: "M", Provider: provider.Provider{ID: "a", Name: "Alpha"}}}
	for _, l := range modelLines(cat) {
		if strings.Contains(l, "Routing groups") {
			t.Fatalf("unexpected section: %q", l)
		}
	}
}

// Usage per provider does not count an agent routed to a routing group.
func TestUsesSkipsRoutingGroups(t *testing.T) {
	mk := func(name, model string) *agent.Agent {
		return &agent.Agent{Name: name, Fields: []agent.Field{{Key: "model", Get: func() string { return model }}}}
	}
	uses := usesOf([]*agent.Agent{
		mk("Claude Code", "magpie/group/mine"),
		mk("Codex", "magpie/a/m"),
		mk("Pi", "group/auto-m"),
	})
	if _, ok := uses["group"]; ok {
		t.Fatalf("group counted: %v", uses)
	}
	if got := uses["a"]; len(got) != 1 || got[0] != "Codex" {
		t.Fatalf("provider a: %v", uses)
	}
}
