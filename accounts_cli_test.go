package main

import (
	"strings"
	"testing"
)

func TestAccountsUnknownAgentShowsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"gemini"},
		{"switch", "gemini", "a@example.com"},
		{"forget", "gemini", "a@example.com"},
		{"add", "gemini"},
	} {
		err := accountsCmd(args)
		if err == nil {
			t.Fatalf("accounts %v: want an error", args)
		}
		for _, want := range []string{"claude", "cc", "codex"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("accounts %v: usage does not mention %q: %v", args, want, err)
			}
		}
	}
}

func TestAccountAgentAliases(t *testing.T) {
	for in, want := range map[string]string{"claude": "claude", "cc": "claude", "CC": "claude", "codex": "codex"} {
		got, err := accountAgent(in)
		if err != nil || got != want {
			t.Fatalf("accountAgent(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
