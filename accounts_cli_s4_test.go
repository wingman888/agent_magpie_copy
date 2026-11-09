package main

import (
	"strings"
	"testing"
)

// An unknown agent is an error whose text carries the usage, naming every
// agent a sign-in can be kept for.
func TestS4AccountsUnknownAgentShowsUsage(t *testing.T) {
	for _, args := range [][]string{{"gemini"}, {"switch", "gemini", "a@example.com"}, {"add", "gemini"}, {"forget", "gemini", "a@example.com"}} {
		err := accountsCmd(args)
		if err == nil {
			t.Fatalf("accounts %v: want an error", args)
		}
		for _, want := range []string{"claude", "cc", "codex"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("accounts %v: usage lacks %q: %v", args, want, err)
			}
		}
	}
}
