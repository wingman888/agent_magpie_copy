package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// An unknown agent fails every accounts verb, and the error carries the usage
// text, which names claude (cc) and codex.
func TestAccountsUnknownAgentFailsWithUsage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	for _, args := range [][]string{
		{"accounts", "foo"},
		{"accounts", "switch", "foo", "a@example.com"},
		{"accounts", "forget", "foo", "a@example.com"},
		{"accounts", "add", "foo"},
	} {
		err := accountsCmd(args)
		if err == nil {
			t.Fatalf("%v: no error", args)
		}
		for _, want := range []string{"claude", "cc", "codex"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%v: usage lacks %q: %v", args, want, err)
			}
		}
	}
}

func TestAccountsListsWithNothingRemembered(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	if err := accountsCmd([]string{"accounts"}); err != nil {
		t.Fatal(err)
	}
	if err := accountsCmd([]string{"accounts", "cc"}); err != nil {
		t.Fatal(err)
	}
}
