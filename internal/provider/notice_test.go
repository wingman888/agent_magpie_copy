package provider

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoticeExplainsClaudeWhenSignedIn(t *testing.T) {
	sandbox(t)
	writeFile(t, filepath.Join(claudeDir(), ".credentials.json"), `{"claudeAiOauth":{"accessToken":"SECRET-TOKEN"}}`)

	ns := Notices()
	if len(ns) != 1 {
		t.Fatalf("Notices() = %+v, want exactly the Claude notice", ns)
	}
	n := ns[0]
	if n.Agent != "claude" || n.Name != "Claude Code" {
		t.Errorf("notice = %+v", n)
	}
	if !strings.Contains(n.Text, "已登录但不提供") || !strings.Contains(n.Text, "凭据") {
		t.Errorf("notice text does not give the reason: %q", n.Text)
	}
	b, _ := json.Marshal(ns)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal("Notices() leaked Claude's credentials")
	}
}

func TestNoNoticeWhenClaudeSignedOut(t *testing.T) {
	sandbox(t)
	if ns := Notices(); len(ns) != 0 {
		t.Errorf("Notices() = %+v with nobody signed in", ns)
	}
}

func TestNoNoticeForOfferedSignIn(t *testing.T) {
	sandbox(t)
	writeCodex(t, "dev@example.com", "plus")
	writeCopilot(t)
	if ns := Notices(); len(ns) != 0 {
		t.Errorf("Notices() = %+v; offered logins need no notice", ns)
	}
}

func TestAccountSourceName(t *testing.T) {
	if got := (Account{Source: "codex"}).SourceName(); got != "Codex" {
		t.Errorf("codex source = %q", got)
	}
	if got := (Account{Source: "copilot"}).SourceName(); got != "GitHub Copilot" {
		t.Errorf("copilot source = %q", got)
	}
}
