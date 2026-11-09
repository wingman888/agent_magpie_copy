package main

import (
	"strings"
	"testing"
)

// Codex is an account provider; giving it an API key is refused with a reason.
func TestFixR7CodexKeyIsRefused(t *testing.T) {
	s5TEnv(t)
	s5TCodexLogin(t)
	err := providerCmd([]string{"provider", "key", "codex", "sk-x"})
	if err == nil || !strings.Contains(err.Error(), "takes no API key") {
		t.Fatalf("err = %v", err)
	}
}
