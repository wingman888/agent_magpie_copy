package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// S4: the routing groups get a section of their own, ahead of the providers.
func TestS4ModelsRoutingGroupsSectionFirst(t *testing.T) {
	alpha := provider.Provider{ID: "alpha", Name: "Alpha"}
	beta := provider.Provider{ID: "beta", Name: "Beta"}
	entries := []provider.Entry{
		{ID: "alpha/m", Model: "m", Name: "M", Provider: alpha},
		{ID: "beta/vendor/m", Model: "vendor/m", Name: "vendor/m", Provider: beta},
		{ID: "group/mine", Model: "group/mine", Name: "Mine", Efforts: []string{"low", "high"}, Group: true},
	}
	var out bytes.Buffer
	printModels(&out, entries)
	s := out.String()

	sec := strings.Index(s, "Routing groups")
	if sec < 0 {
		t.Fatalf("no Routing groups section:\n%s", s)
	}
	first := strings.Index(s, "Alpha")
	if first < 0 || sec > first {
		t.Fatalf("Routing groups must come before the providers:\n%s", s)
	}
	if strings.Index(s, "group/mine") < sec || strings.Index(s, "group/mine") > first {
		t.Fatalf("group/mine should sit in the Routing groups section:\n%s", s)
	}
	if !strings.Contains(s, "Mine") || !strings.Contains(s, "low/high") {
		t.Fatalf("group name or efforts missing:\n%s", s)
	}
	if strings.Index(s, "Alpha") > strings.Index(s, "Beta") {
		t.Fatalf("provider order changed:\n%s", s)
	}
}

// S4: without groups there is no section, and provider rows are as before.
func TestS4ModelsNoGroupsNoSection(t *testing.T) {
	entries := []provider.Entry{
		{ID: "alpha/m", Model: "m", Name: "m", Provider: provider.Provider{ID: "alpha", Name: "Alpha"}},
	}
	var out bytes.Buffer
	printModels(&out, entries)
	s := out.String()
	if strings.Contains(s, "Routing groups") {
		t.Fatalf("unexpected section:\n%s", s)
	}
	if !strings.Contains(s, "Alpha") || !strings.Contains(s, "alpha/m") {
		t.Fatalf("provider section missing:\n%s", s)
	}
}

// S4: an agent routed to a routing group is not counted as a provider's user.
func TestS4UsesSkipsRoutingGroups(t *testing.T) {
	agentWith := func(name, model string) *agent.Agent {
		return &agent.Agent{Name: name, Fields: []agent.Field{{Get: func() string { return model }}}}
	}
	as := []*agent.Agent{
		agentWith("Grouped", "magpie/group/mine"),
		agentWith("Direct", "magpie/alpha/m"),
		agentWith("Bare group", "group/auto-m"),
		{Name: "No fields"},
	}
	got := usesIn(as)
	if len(got["group"]) != 0 {
		t.Fatalf("group configs counted as provider users: %v", got)
	}
	if _, ok := got["alpha"]; !ok || len(got["alpha"]) != 1 || got["alpha"][0] != "Direct" {
		t.Fatalf("alpha users = %v, want [Direct]", got["alpha"])
	}
	if len(got) != 1 {
		t.Fatalf("unexpected keys: %v", got)
	}
}
