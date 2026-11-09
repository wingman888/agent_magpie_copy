package main

import (
	"strings"
	"testing"
)

// s4AccountsUsageCase checks an error from `magpie accounts` names the agents and the usage.
func s4AccountsUsageCase(t *testing.T, args []string) {
	t.Helper()
	err := accountsCmd(args)
	if err == nil {
		t.Fatalf("accounts %v: no error, want a non-zero exit", args)
	}
	msg := err.Error()
	for _, want := range []string{"claude", "cc", "codex", "usage:"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("accounts %v: error lacks %q:\n%s", args, want, msg)
		}
	}
}

func TestS4AccountsUnknownAgentIsUsageError(t *testing.T) {
	s4AccountsUsageCase(t, []string{"gemini"})
	s4AccountsUsageCase(t, []string{"switch", "gemini", "a@x.com"})
	s4AccountsUsageCase(t, []string{"forget", "nope", "a@x.com"})
	s4AccountsUsageCase(t, []string{"add", "nope"})
}

func TestS4AccountsBadArity(t *testing.T) {
	s4AccountsUsageCase(t, []string{"switch", "claude"})
	s4AccountsUsageCase(t, []string{"forget"})
	s4AccountsUsageCase(t, []string{"add"})
}
