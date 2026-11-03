package main

import (
	"strings"
	"testing"
)

// an unknown agent is an error that carries the usage, naming claude (cc) and codex
func TestAccountsUnknownAgentShowsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"gemini"},
		{"switch", "gemini", "a@example.com"},
		{"on", "gemini", "a@example.com"},
		{"forget", "nope", "a@example.com"},
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
