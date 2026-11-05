package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRemoveHidesAccountAndKeepsItsPicks(t *testing.T) {
	home := signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(home, ".codex", "auth.json")
	before, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := Remove("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("removed codex still listed")
	}
	if _, err := Find("codex"); err == nil {
		t.Fatal("removed codex still found")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves")
	}
	for _, e := range Catalog() {
		if strings.HasPrefix(e.ID, "codex/") {
			t.Fatalf("catalog still has %s", e.ID)
		}
	}
	if !Removed("codex") {
		t.Fatal("Removed(codex) = false")
	}
	if x := Excluded(); len(x) != 1 || x[0].Agent != "codex" || x[0].Provider != "codex" || !strings.Contains(x[0].Why, "magpie provider add codex") {
		t.Fatalf("excluded: %+v", x)
	}
	// the agent's own sign-in is untouched
	after, err := os.ReadFile(authPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("codex auth.json changed: %v", err)
	}
	// copilot is still there
	if _, ok := find(All(), "copilot"); !ok {
		t.Fatal("copilot disappeared with codex")
	}

	if err := Restore("codex"); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "codex")
	if !ok || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "gpt-5.5" {
		t.Fatalf("restored codex lost its picks: %+v %v", p, ok)
	}
	if Removed("codex") || len(Excluded()) != 0 {
		t.Fatal("restore left the removal behind")
	}
}

func TestRemoveCustomProviderDeletes(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "acme", Name: "Acme", Chat: "https://api.acme.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := Remove("acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := Find("acme"); err == nil {
		t.Fatal("custom provider still there after rm")
	}
	if Removed("acme") {
		t.Fatal("a deleted custom provider was recorded as removed")
	}
}

func TestSaveImportedAllowsNoKey(t *testing.T) {
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	p := Provider{ID: "acme", Name: "Acme", Chat: "https://api.acme.example/v1"}
	if err := Save(p); err == nil {
		t.Fatal("Save accepted a provider with no key")
	}
	if err := SaveImported(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("acme")
	if err != nil || got.Ready() {
		t.Fatalf("imported without key: %+v %v", got, err)
	}
}

// fakeClaude puts a `claude` script first on PATH with the given shell body.
func fakeClaude(t *testing.T, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestClaudeSignedOutIsNotAProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	// residual credentials, but Claude Code says signed out and exits non-zero
	fakeClaude(t, `printf '%s' '{"loggedIn":false}'; exit 1`)
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out Claude Code still listed as a provider")
	}
}

func TestClaudeStatusUnparseableKeepsCredentialJudgement(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	fakeClaude(t, `printf '%s' 'not json at all'; exit 3`)
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude dropped although its status could not be read")
	}
}

func TestClaudeStatusLoggedInStaysAProvider(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))

	fakeClaude(t, `printf '%s' '{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}'`)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" {
		t.Fatalf("claude: %+v %v", p, ok)
	}
}
