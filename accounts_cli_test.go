package main

import (
	"strings"
	"testing"
)

// An unknown agent is an error (so the process exits non-zero) and the error
// carries the usage, which names both agents and claude's alias.
func TestAccountsCmdUnknownAgentIsUsageError(t *testing.T) {
	for _, args := range [][]string{{"gemini"}, {"switch", "gemini", "a@x.com"}, {"forget", "gemini", "a@x.com"}, {"add", "gemini"}} {
		err := accountsCmd(args)
		if err == nil {
			t.Fatalf("accountsCmd(%v) accepted an unknown agent", args)
		}
		for _, want := range []string{"claude", "cc", "codex", "usage"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("accountsCmd(%v) usage lacks %q:\n%s", args, want, err)
			}
		}
	}
}
