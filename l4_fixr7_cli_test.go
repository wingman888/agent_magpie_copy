package main

import (
	"strings"
	"testing"
)

// Codex is an account provider; giving it an API key is refused with a reason.
func TestL4FixR7CodexKeyIsRefused(t *testing.T) {
	l4S5TEnv(t)
	l4S5TCodexLogin(t)
	err := providerCmd([]string{"provider", "key", "codex", "sk-x"})
	if err == nil || !strings.Contains(err.Error(), "takes no API key") {
		t.Fatalf("err = %v", err)
	}
}
