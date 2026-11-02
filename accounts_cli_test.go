package main

import (
	"strings"
	"testing"
)

// An unknown agent is an error carrying the usage text, for every subcommand.
func TestAccountsUnknownAgent(t *testing.T) {
	for _, args := range [][]string{
		{"accounts", "gemini"},
		{"accounts", "switch", "gemini", "a@b.c"},
		{"accounts", "forget", "gemini", "a@b.c"},
		{"accounts", "add", "gemini"},
		{"accounts", "switch"},
	} {
		err := run(args)
		if err == nil {
			t.Fatalf("%v: no error", args)
		}
		for _, want := range []string{"claude", "cc", "codex", "usage:"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: error lacks %q: %v", args, want, err)
			}
		}
	}
}
