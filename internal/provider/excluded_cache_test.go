package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExcludedFollowsCredsFileNow_Ecx checks that Excluded does not keep a
// stale answer for about a minute after Claude Code's sign-in file is created
// or deleted: the cache key includes the file's state.
func TestExcludedFollowsCredsFileNow_Ecx(t *testing.T) {
	home := acctSandbox(t)
	creds := filepath.Join(home, ".claude", ".credentials.json")

	if got := Excluded(); len(got) != 0 {
		t.Fatalf("no claude sign-in: want no exclusions, got %+v", got)
	}

	acctWriteFile(t, creds, `{"claudeAiOauth":{"accessToken":"secret"}}`)
	got := Excluded()
	if len(got) != 1 || got[0].Agent != "claude" || got[0].Why == "" {
		t.Fatalf("claude sign-in present: want one claude exclusion, got %+v", got)
	}

	if err := os.Remove(creds); err != nil {
		t.Fatal(err)
	}
	if got := Excluded(); len(got) != 0 {
		t.Fatalf("after sign-out: want no exclusions at once, got %+v", got)
	}
}

// TestExcludedFollowsClaudeConfigDir_Ecx checks that a changed
// CLAUDE_CONFIG_DIR is honored at once, not after the cache expires.
func TestExcludedFollowsClaudeConfigDir_Ecx(t *testing.T) {
	home := acctSandbox(t)
	acctWriteFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{}`)
	if len(Excluded()) != 1 {
		t.Fatal("want claude exclusion from ~/.claude")
	}

	other := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", other)
	if got := Excluded(); len(got) != 0 {
		t.Fatalf("CLAUDE_CONFIG_DIR points at an empty dir: want no exclusions, got %+v", got)
	}
}
