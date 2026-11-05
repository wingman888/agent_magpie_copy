package main

import (
	"strings"
	"testing"
)

// An unknown agent, or a bad argument count, is an error that carries the
// usage text, which names both agents and the cc alias.
func TestAccountsUnknownAgentShowsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"gemini"},
		{"switch", "gemini", "me@example.com"},
		{"forget", "gemini", "me@example.com"},
		{"add", "gemini"},
		{"switch", "claude"},
		{"add"},
	} {
		err := accountsCmd(args)
		if err == nil {
			t.Fatalf("accountsCmd(%q) succeeded, want an error", args)
		}
		msg := err.Error()
		for _, want := range []string{"claude", "cc", "codex", "usage:"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("accountsCmd(%q) error lacks %q:\n%s", args, want, msg)
			}
		}
	}
}

func TestAgentLabels(t *testing.T) {
	if agentLabel("claude") != "Claude Code" || agentLabel("codex") != "Codex" {
		t.Fatal("unexpected agent labels")
	}
}
