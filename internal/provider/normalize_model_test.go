package provider

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// The same model spelled by different providers has one key.
func TestF09ModelKeySameModel(t *testing.T) {
	same := []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"  claude-opus-5.5  ",
	}
	want := modelKey(same[0])
	for _, id := range same {
		if got := modelKey(id); got != want {
			t.Errorf("modelKey(%q) = %q, want %q (same as %q)", id, got, want, same[0])
		}
	}
	if want != "claude-opus-5-5" {
		t.Errorf("key of the plain spelling = %q", want)
	}
}

// A variant after the colon is a different model; the dot rules leave other
// dots alone.
func TestF09ModelKeyKeepsVariantsAndOtherDots(t *testing.T) {
	if modelKey("x:batch") == modelKey("x") {
		t.Errorf("x:batch and x must differ, both keyed %q", modelKey("x"))
	}
	if got := modelKey("v1.beta"); got != "v1.beta" {
		t.Errorf("modelKey(v1.beta) = %q, want unchanged", got)
	}
	if got := modelKey("gpt-5.5:Batch"); got != "gpt-5-5:batch" {
		t.Errorf("modelKey(gpt-5.5:Batch) = %q", got)
	}
}

// Only an eight-digit date after a hyphen is dropped.
func TestF09ModelKeyDateSuffix(t *testing.T) {
	cases := map[string]string{
		"model-20260801":  "model",
		"model-2026080":   "model-2026080",
		"model20260801":   "model20260801",
		"model-2026080a":  "model-2026080a",
		"anthropic/m-5.5": "m-5-5",
	}
	for in, want := range cases {
		if got := modelKey(in); got != want {
			t.Errorf("modelKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestF09AutoGroupsFormsOnlySharedModels(t *testing.T) {
	m := func(id, name string) catalog.Model { return catalog.Model{ID: id, Name: name} }
	sources := []ProviderModels{
		{Provider: "a", Models: []catalog.Model{m("claude-opus-5-5", "Claude Opus 5.5"), m("only-a", "")}},
		{Provider: "b", Models: []catalog.Model{m("anthropic/claude-opus-5.5", ""), m("Claude-Opus-5.5-20260801", "")}},
		{Provider: "c", Models: []catalog.Model{m("x:batch", ""), m("claude-opus-5.5:batch", "")}},
		{Provider: "d", Models: []catalog.Model{m("x", "")}},
	}
	got := AutoGroups(sources)

	want := []AutoGroup{{
		ID:   "auto-claude-opus-5-5",
		Name: "Claude Opus 5.5",
		Members: []AutoMember{
			{Provider: "a", Model: "claude-opus-5-5"},
			{Provider: "b", Model: "anthropic/claude-opus-5.5"},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AutoGroups:\n got  %+v\n want %+v", got, want)
	}
}

// Without a formal display name the group takes its first member's id; a
// provider's duplicate listing counts once; a name listed by one provider
// only does not group.
func TestF09AutoGroupsNameAndDuplicates(t *testing.T) {
	sources := []ProviderModels{
		{Provider: "a", Models: []catalog.Model{{ID: "m-1"}, {ID: "M-1"}, {ID: "solo"}}},
		{Provider: "b", Models: []catalog.Model{{ID: "vendor/m-1", Name: "vendor/m-1"}}},
		{Provider: "c", Models: []catalog.Model{{ID: "solo-c"}}},
	}
	got := AutoGroups(sources)
	if len(got) != 1 {
		t.Fatalf("want one group, got %+v", got)
	}
	g := got[0]
	if g.ID != "auto-m-1" || g.Name != "m-1" {
		t.Errorf("id/name = %q/%q, want auto-m-1/m-1", g.ID, g.Name)
	}
	want := []AutoMember{{Provider: "a", Model: "m-1"}, {Provider: "b", Model: "vendor/m-1"}}
	if !reflect.DeepEqual(g.Members, want) {
		t.Errorf("members = %+v, want %+v", g.Members, want)
	}
}

// Different names never group automatically, even when one is a prefix of
// another.
func TestF09AutoGroupsDifferentNamesStayApart(t *testing.T) {
	sources := []ProviderModels{
		{Provider: "a", Models: []catalog.Model{{ID: "claude-opus-5-5"}}},
		{Provider: "b", Models: []catalog.Model{{ID: "claude-opus-5-5-mini"}}},
	}
	if got := AutoGroups(sources); len(got) != 0 {
		t.Fatalf("different models grouped: %+v", got)
	}
}
