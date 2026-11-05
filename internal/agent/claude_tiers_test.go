package agent

import (
	"strings"
	"testing"
)

// A pin equal to the main model still counts as chosen, and with every tier
// equal to the main model the shared subagent model comes back.
func TestClaudePinEqualToMainRestoresUnified(t *testing.T) {
	a, path := claudeHome(t, false)
	setField(t, a, "model", "deepseek/pro")
	setField(t, a, "haiku", "deepseek/flash")
	if err := setField(t, a, "haiku", "deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if got := env(t, path, "CLAUDE_CODE_SUBAGENT_MODEL"); got != "deepseek/pro" {
		t.Errorf("subagent model = %q, want unified deepseek/pro", got)
	}
	if got := env(t, path, "ANTHROPIC_SMALL_FAST_MODEL"); got != "deepseek/pro" {
		t.Errorf("small fast = %q, want deepseek/pro", got)
	}
	if got := a.Field("haiku").Get(); got != "deepseek/pro" {
		t.Errorf("haiku pin = %q, want it kept as chosen", got)
	}
	// changing the main model away from the pin cancels the unified subagent
	// again, and the pin stays put
	if err := setField(t, a, "model", "deepseek/chat"); err != nil {
		t.Fatal(err)
	}
	if got := env(t, path, "CLAUDE_CODE_SUBAGENT_MODEL"); got != "" {
		t.Errorf("subagent model = %q after main changed, want none", got)
	}
	if got := env(t, path, "ANTHROPIC_DEFAULT_HAIKU_MODEL"); got != "deepseek/pro" {
		t.Errorf("pinned haiku = %q, want deepseek/pro", got)
	}
}

// `magpie claude opus` with no model is a usage error, not a main model
// called "opus", and it leaves the routing alone.
func TestClaudeBareTierNameIsUsageError(t *testing.T) {
	a, path := claudeHome(t, false)
	setField(t, a, "model", "deepseek/pro")
	_, err := BareTarget(a, "opus")
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("BareTarget(opus) err = %v, want a usage error", err)
	}
	if got := env(t, path, "ANTHROPIC_MODEL"); got != "deepseek/pro" {
		t.Errorf("ANTHROPIC_MODEL = %q, routing was disturbed", got)
	}
}

func TestBareTargetRoutesModelsToMainAndTiersToTheirField(t *testing.T) {
	a, _ := claudeHome(t, false)
	cases := []struct{ value, want string }{
		{"deepseek/flash", "model"}, // a catalog model: the main model
		{"deepseek/pro", "model"},
		{"claude-opus-4", "model"}, // unknown values fall to the main model as before
	}
	for _, c := range cases {
		got, err := BareTarget(a, c.value)
		if err != nil {
			t.Errorf("BareTarget(%q) err = %v", c.value, err)
			continue
		}
		if got != c.want {
			t.Errorf("BareTarget(%q) = %q, want %q", c.value, got, c.want)
		}
	}
	// a model offered by a tier's picker but also by the main picker is a model
	if f := FieldForValue(a, "deepseek/flash"); f != nil {
		t.Errorf("FieldForValue(deepseek/flash) = %q, want nil (main model wins)", f.Key)
	}
}
