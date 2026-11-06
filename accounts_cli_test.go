package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// signedInCodex writes a Codex login for one account into the isolated home.
func signedInCodex(t *testing.T, home, email string) {
	t.Helper()
	claims := map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-1"}}
	b, _ := json.Marshal(claims)
	id := "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
	auth := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": id, "access_token": "h.e30.sig", "refresh_token": "r", "account_id": "acct-1"}}
	out, _ := json.Marshal(auth)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountsCommandRefusalsAndUsage(t *testing.T) {
	isolateCLI(t)
	home := os.Getenv("HOME")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	signedInCodex(t, home, "me@example.com")

	err := accountsCmd([]string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "claude (cc)") || !strings.Contains(err.Error(), "codex") {
		t.Fatalf("unknown agent error = %v", err)
	}
	for _, args := range [][]string{
		{"switch", "nope", "x@example.com"},
		{"forget", "nope", "x@example.com"},
		{"add", "nope"},
	} {
		if err := accountsCmd(args); err == nil || !strings.Contains(err.Error(), "claude (cc)") {
			t.Fatalf("%v: error = %v, want unknown agent with usage", args, err)
		}
	}
	if err := accountsCmd([]string{"forget", "codex", "me@example.com"}); err == nil {
		t.Fatal("forgot the account signed in to")
	}
	if err := accountsCmd([]string{"switch", "cc", "nobody@example.com"}); err == nil {
		t.Fatal("switched to an unknown account")
	}
	if err := accountsCmd([]string{"switch", "codex"}); err == nil {
		t.Fatal("switch without an email was accepted")
	}
	if err := accountsCmd(nil); err != nil {
		t.Fatalf("list all: %v", err)
	}
	if err := accountsCmd([]string{"codex"}); err != nil {
		t.Fatalf("list codex: %v", err)
	}
}

func TestAccountsCommandRunsFromMain(t *testing.T) {
	isolateCLI(t)
	home := os.Getenv("HOME")
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "no-claude"))
	if err := run([]string{"accounts", "gemini"}); err == nil {
		t.Fatal("magpie accounts gemini exited cleanly")
	}
}
