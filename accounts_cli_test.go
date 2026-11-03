package main

import (
	"strings"
	"testing"
)

func TestAccountsUnknownAgentShowsUsage(t *testing.T) {
	for _, args := range [][]string{
		{"accounts", "gemini"},
		{"accounts", "switch", "gemini", "a@example.com"},
		{"accounts", "add", "gemini"},
		{"accounts", "forget", "gemini", "a@example.com"},
	} {
		err := accountsCmd(args)
		if err == nil {
			t.Fatalf("%v: no error for an unknown agent", args)
		}
		if !strings.Contains(err.Error(), "unknown agent") || !strings.Contains(err.Error(), "claude (cc)") || !strings.Contains(err.Error(), "codex") {
			t.Fatalf("%v: error lacks usage: %v", args, err)
		}
	}
}
