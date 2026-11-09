package provider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// s4ResetClaudeStatus forgets the cached `claude auth status` answer.
func s4ResetClaudeStatus() {
	claudeStatusMu.Lock()
	claudeStatusAt = time.Time{}
	claudeStatusUser, claudeStatusPlan, claudeStatusOut = "", "", false
	claudeStatusMu.Unlock()
}

// s4StubClaude makes a fake `claude` binary visible on PATH and answers
// `claude auth status --json` with out and runErr instead of running it.
func s4StubClaude(t *testing.T, out string, runErr error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	old := claudeAuthStatus
	claudeAuthStatus = func(context.Context, string) ([]byte, error) { return []byte(out), runErr }
	s4ResetClaudeStatus()
	t.Cleanup(func() {
		claudeAuthStatus = old
		s4ResetClaudeStatus()
	})
}

func TestS4RemoveCodexHidesAndRestoresPicks(t *testing.T) {
	home := signIn(t)
	if err := Save(Provider{ID: "codex", Name: "x", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(home, ".codex", "auth.json")
	before, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := Remove("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("codex still listed after rm")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("codex/gpt-5.5 still resolves after rm")
	}
	if ex := Excluded(); len(ex) != 1 || ex[0].Agent != "codex" || ex[0].Provider != "codex" {
		t.Fatalf("Excluded() = %+v", ex)
	}
	if after, err := os.ReadFile(credPath); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("agent credentials changed by rm: %v", err)
	}

	// bringing it back with no new picks keeps the old ones
	if err := Save(Provider{ID: "codex", Name: "codex"}); err != nil {
		t.Fatal(err)
	}
	if len(Excluded()) != 0 {
		t.Fatalf("still excluded after add: %+v", Excluded())
	}
	p, ok := find(All(), "codex")
	if !ok || p.Account == nil || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "gpt-5.5" {
		t.Fatalf("restored codex: %+v", p)
	}
}

func TestS4RemoveIsIdempotentAndKeepsCredentials(t *testing.T) {
	signIn(t)
	if err := Hide("codex"); err != nil {
		t.Fatal(err)
	}
	if err := Hide("codex"); err != nil {
		t.Fatal(err)
	}
	if ex := Excluded(); len(ex) != 1 {
		t.Fatalf("Excluded() after twice = %+v", ex)
	}
	if err := Hide("no-such-account"); err == nil {
		t.Fatal("hiding an unknown account should fail")
	}
}

func TestS4RemoveClaudeAccount(t *testing.T) {
	home := claudeHome(t)
	creds := claudeSignIn(t, home, time.Now().Add(time.Hour))
	s4StubClaude(t, `{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}`, nil)
	before, _ := os.ReadFile(creds)

	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude not listed before rm")
	}
	if err := Remove("claude"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude still listed after rm")
	}
	if after, err := os.ReadFile(creds); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("Claude Code credentials changed by rm: %v", err)
	}
	if err := Save(Provider{ID: "claude", Name: "claude"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "claude"); !ok {
		t.Fatal("claude not back after add")
	}
}

func TestS4ClaudeSignedOutIsNotProvider(t *testing.T) {
	home := claudeHome(t)
	creds := claudeSignIn(t, home, time.Now().Add(time.Hour))
	// Claude Code exits non-zero when signed out, and says so in the JSON.
	s4StubClaude(t, `{"loggedIn":false}`, errors.New("exit status 1"))
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out claude listed while its credential remains")
	}
	if _, err := os.Stat(creds); err != nil {
		t.Fatalf("credential file should be left alone: %v", err)
	}
}

func TestS4ClaudeStatusUnknownKeepsJudgment(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	cases := []struct {
		name, out string
		err       error
	}{
		{"unparsable output", "not json", nil},
		{"empty output with failure", "", errors.New("boom")},
		{"no loggedIn field", `{}`, nil},
	}
	for _, c := range cases {
		s4StubClaude(t, c.out, c.err)
		if _, ok := find(All(), "claude"); !ok {
			t.Fatalf("%s: claude dropped, want previous judgment (listed)", c.name)
		}
	}
}

func TestS4ClaudeLoggedInUsesStatusEmail(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	s4StubClaude(t, `{"loggedIn":true,"email":"me@example.com","subscriptionType":"max"}`, nil)
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "me@example.com" {
		t.Fatalf("claude: %+v %v", p, ok)
	}
}
