package main

import (
	"strings"
	"testing"
)

func TestAccountAgentNames(t *testing.T) {
	for in, want := range map[string]string{"claude": "claude", "cc": "claude", "CODEX": "codex", "Codex": "codex"} {
		if got, err := accountAgent(in); err != nil || got != want {
			t.Fatalf("accountAgent(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	_, err := accountAgent("gemini")
	if err == nil || !strings.Contains(err.Error(), "claude (cc)") || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("unknown agent error lacks usage: %v", err)
	}
}
