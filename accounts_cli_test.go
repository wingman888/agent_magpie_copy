package main

import (
	"strings"
	"testing"
)

// Unknown agents are an error that prints the usage, which names both agents.
func TestAccountsCmdUnknownAgentPrintsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"accounts", "gemini"},
		{"accounts", "switch", "gemini", "a@example.com"},
		{"accounts", "forget", "gemini", "a@example.com"},
		{"accounts", "add", "gemini"},
	} {
		err := accountsCmd(args)
		if err == nil {
			t.Fatalf("%v: no error", args)
		}
		msg := err.Error()
		for _, want := range []string{"claude", "cc", "codex"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("%v: usage lacks %q: %s", args, want, msg)
			}
		}
	}
}

func TestAccountsCmdWrongArgCountIsAnError(t *testing.T) {
	for _, args := range [][]string{
		{"accounts", "switch", "claude"},
		{"accounts", "forget", "codex"},
		{"accounts", "add"},
		{"accounts", "codex", "extra"},
	} {
		if err := accountsCmd(args); err == nil {
			t.Fatalf("%v: no error", args)
		}
	}
}
