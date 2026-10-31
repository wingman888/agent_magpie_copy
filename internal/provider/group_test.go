package provider

import "testing"

// Vendors spell the same model differently; a group magpie finds joins
// them all.
func TestAutoGroupsSameModel(t *testing.T) {
	for in, want := range map[string]string{
		"claude-opus-5-5":                 "claude-opus-5-5",
		"anthropic/claude-opus-5.5":       "claude-opus-5-5",
		"Claude-Opus-5.5":                 "claude-opus-5-5",
		"claude-opus-5-5-20260801":        "claude-opus-5-5",
		"anthropic/claude-opus-5.5:batch": "claude-opus-5-5:batch",
		"gpt-5.1-codex":                   "gpt-5-1-codex",
		"v1.beta":                         "v1.beta",
	} {
		if got := sameModel(in); got != want {
			t.Errorf("sameModel(%q) = %q, want %q", in, got, want)
		}
	}
	e := func(p, m, name string) Entry {
		return Entry{ID: p + "/" + m, Model: m, Name: name, Provider: Provider{ID: p}}
	}
	gs := autoGroups([]Entry{
		e("openrouter", "anthropic/claude-opus-5.5", "anthropic/claude-opus-5.5"),
		e("copilot", "claude-opus-5.5", "claude-opus-5.5"),
		e("claude", "claude-opus-5-5", "Claude Opus 5.5"),
	})
	if len(gs) != 1 || gs[0].ID != "auto-claude-opus-5-5" || len(gs[0].Members) != 3 || gs[0].Name != "Claude Opus 5.5" {
		t.Fatalf("groups: %+v", gs)
	}

}
