package main

import (
	"strings"
	"testing"
)

// An unknown agent fails every accounts subcommand, and the error carries the
// usage that names claude (cc) and codex.
func TestAccountsRejectsUnknownAgent(t *testing.T) {
	for _, args := range [][]string{
		{"gemini"},
		{"switch", "gemini", "a@example.com"},
		{"forget", "gemini", "a@example.com"},
		{"add", "gemini"},
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
