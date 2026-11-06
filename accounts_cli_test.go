package main

import (
	"strings"
	"testing"
)

func TestAccountsUnknownAgentIsUsageError(t *testing.T) {
	cliHome(t)
	for _, args := range [][]string{
		{"gemini"},
		{"switch", "gemini", "a@x.com"},
		{"forget", "gemini", "a@x.com"},
		{"add", "gemini"},
		{"on", "gemini", "a@x.com"},
	} {
		err := accountsCmd(args)
		if err == nil {
			t.Errorf("%v: no error for an unknown agent", args)
			continue
		}
		for _, want := range []string{"claude", "cc", "codex"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: usage does not mention %q: %v", args, want, err)
			}
		}
	}
}

func TestAccountsListWithNothingSaved(t *testing.T) {
	cliHome(t)
	if err := accountsCmd(nil); err != nil {
		t.Fatalf("accounts with nothing saved: %v", err)
	}
	if err := accountsCmd([]string{"cc"}); err != nil {
		t.Fatalf("accounts cc: %v", err)
	}
}

func TestAccountsArgumentCounts(t *testing.T) {
	cliHome(t)
	for _, args := range [][]string{
		{"switch", "codex"},
		{"forget", "codex"},
		{"add"},
		{"codex", "extra"},
	} {
		if err := accountsCmd(args); err == nil {
			t.Errorf("%v: wrong argument count accepted", args)
		}
	}
}
