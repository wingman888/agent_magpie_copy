package profile

import (
	"reflect"
	"testing"
)

// A profile applies providers first, then models, then everything else, so a
// Claude Code tier is set on top of the model the profile names.
func TestApplyOrderL3x(t *testing.T) {
	keys := []string{"claude.haiku", "codex.effort", "claude.model", "codex.provider", "claude.fable", "pi.model", "pi.effort"}
	applyOrderL3x(keys)
	want := []string{"codex.provider", "claude.model", "pi.model", "claude.fable", "claude.haiku", "codex.effort", "pi.effort"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("order: %v", keys)
	}
}
