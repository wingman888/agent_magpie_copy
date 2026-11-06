package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuditClaudeNoticeSaysNotProvided: a signed-in Claude Code is said to be
// signed in but not provided, and the notice never shows its credentials.
func TestZzAuditClaudeNoticeSaysNotProvided(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"accessToken":"SECRET"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ns := Notices()
	if len(ns) != 1 {
		t.Fatalf("notices = %+v", ns)
	}
	txt := ns[0].Text
	if !strings.Contains(txt, "已登录") || !strings.Contains(txt, "不提供") {
		t.Errorf("notice does not say signed in but not provided: %s", txt)
	}
	if strings.Contains(txt, "SECRET") {
		t.Error("the notice leaks a credential")
	}
}
