package provider

import "testing"

// The same model written alike is one model: case, a vendor prefix, dots
// between version numbers and a trailing date don't make it another.
func TestCanonicalModel(t *testing.T) {
	want := "claude-opus-5-5"
	for _, id := range []string{
		"claude-opus-5-5",
		"anthropic/claude-opus-5.5",
		"Claude-Opus-5.5",
		"claude-opus-5-5-20260801",
		"anthropic/claude-opus-5.5-20260801",
		"claude-opus-5.5",
	} {
		if got := canonicalModel(id); got != want {
			t.Errorf("canonicalModel(%q) = %q, want %q", id, got, want)
		}
	}
}

// A colon variant is another model, and a dot that is not between two
// numbers is kept.
func TestCanonicalModelKeeps(t *testing.T) {
	if got := canonicalModel("m:batch"); got != "m:batch" || got == canonicalModel("m") {
		t.Errorf("canonicalModel(m:batch) = %q, must differ from m", got)
	}
	if got := canonicalModel("v1.beta"); got != "v1.beta" {
		t.Errorf("canonicalModel(v1.beta) = %q, want unchanged", got)
	}
	if got := canonicalModel("gpt-4.1"); got != "gpt-4-1" {
		t.Errorf("canonicalModel(gpt-4.1) = %q, want gpt-4-1", got)
	}
}

func TestAutoGroupID(t *testing.T) {
	if got := autoGroupID("claude-opus-5-5"); got != "auto-claude-opus-5-5" {
		t.Errorf("got %q", got)
	}
	if got := autoGroupID("m:batch"); got != "auto-m-batch" {
		t.Errorf("got %q", got)
	}
	if got := autoGroupID("v1.beta_x"); got != "auto-v1-beta-x" {
		t.Errorf("got %q", got)
	}
}

// The group takes the official name of the first member that has one, and
// otherwise the canonical form capitalized.
func TestGroupName(t *testing.T) {
	refs := []modelRef{
		{Provider: "a", ID: "claude-opus-5-5"},
		{Provider: "b", ID: "anthropic/claude-opus-5.5", Name: "Claude Opus 5.5"},
	}
	if got := groupName("claude-opus-5-5", refs); got != "Claude Opus 5.5" {
		t.Errorf("got %q", got)
	}
	// A name that is only the id doesn't count as official.
	refs = []modelRef{{Provider: "a", ID: "m", Name: "m"}}
	if got := groupName("claude-opus-5-5", nil); got != "Claude Opus 5-5" {
		t.Errorf("no members: got %q", got)
	}
	if got := groupName("claude-opus-5-5", refs); got != "Claude Opus 5-5" {
		t.Errorf("id as name: got %q", got)
	}
}
