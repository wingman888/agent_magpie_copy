package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestS3AccountsCLIRefusesUnknownAgents(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	for _, args := range [][]string{
		{"gemini"},
		{"switch", "gemini", "x@example.com"},
		{"on", "gemini", "x@example.com"},
		{"forget", "gemini", "x@example.com"},
		{"add", "gemini"},
	} {
		err := magAccountsCmd(args)
		if err == nil {
			t.Fatalf("accounts %v: no error for an unknown agent", args)
		}
		for _, want := range []string{"claude", "cc", "codex"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("accounts %v: usage does not name %q: %v", args, want, err)
			}
		}
	}
}

func TestS3AccountsCLIListsNothingWithoutSignIns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	if err := magAccountsCmd(nil); err != nil {
		t.Fatal(err)
	}
}
