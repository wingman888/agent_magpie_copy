package main

import (
	"strings"
	"testing"
)

// An unknown agent is an error whose text carries the usage, naming claude,
// its alias cc, and codex. main turns any error into exit status 1.
func TestAcctcliUnknownAgentFailsWithUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, args := range [][]string{
		{"accounts", "gemini"},
		{"accounts", "switch", "gemini", "x@example.com"},
		{"accounts", "forget", "gemini", "x@example.com"},
		{"accounts", "add", "gemini"},
		{"accounts", "on", "gemini", "x@example.com"},
		{"accounts", "off", "gemini", "x@example.com"},
	} {
		err := accountsCmd(args)
		if err == nil {
			t.Errorf("%v: no error", args)
			continue
		}
		for _, word := range []string{"claude", "cc", "codex"} {
			if !strings.Contains(err.Error(), word) {
				t.Errorf("%v: usage lacks %q: %v", args, word, err)
			}
		}
	}
}

// With nothing remembered, listing both agents succeeds and says so.
func TestAcctcliListsWithNothingRemembered(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if err := accountsCmd([]string{"accounts"}); err != nil {
		t.Fatal(err)
	}
	if err := accountsCmd([]string{"accounts", "cc"}); err != nil {
		t.Fatal(err)
	}
}
