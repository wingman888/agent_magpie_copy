package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/provider"
)

// cli5Token is a Claude Code credential value that must never reach output.
const cli5Token = "cli5-secret-claude-token"

func cli5JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestCli5ProvidersShowAccountAndClaudeNote(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	// Codex: a ChatGPT login
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	id := cli5JWT(map[string]any{"email": "cli5@example.test",
		"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
	acc := cli5JWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": id, "access_token": acc, "refresh_token": "r", "account_id": "acct"}})
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}

	// Claude Code: signed in, with a credential that must stay unread
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(`{"token":"`+cli5Token+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var codexP *provider.Provider
	for _, p := range provider.All() {
		if p.ID == provider.ClaudeID {
			t.Fatal("claude must never be listed as a provider")
		}
		if p.ID == provider.CodexID {
			p := p
			codexP = &p
		}
	}
	if codexP == nil {
		t.Fatal("codex should be listed while signed in")
	}
	if got := keyCell(*codexP); !strings.Contains(got, "signed in as cli5@example.test") {
		t.Fatalf("key cell = %q, want signed in as <email>", got)
	}
	if got := accountLine(codexP.Account); got != "cli5@example.test (plus) · from Codex" {
		t.Fatalf("account line = %q", got)
	}
	if !codexP.IsAccount() || codexP.Preset != "" {
		t.Fatal("codex should be an account provider, not a custom one")
	}

	notes := strings.Join(provider.Notes(), "\n")
	for _, want := range []string{"已登录", "不提供", "原因"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("claude note %q lacks %q", notes, want)
		}
	}
	if strings.Contains(notes, cli5Token) {
		t.Fatal("claude note leaks the credential")
	}

	// the CLI renders without error and the credential never appears in it
	if err := providers(); err != nil {
		t.Fatal(err)
	}
	if err := showProvider(*codexP); err != nil {
		t.Fatal(err)
	}
	dump, _ := json.Marshal(provider.All())
	if strings.Contains(string(dump), cli5Token) {
		t.Fatal("provider list leaks the Claude credential")
	}
}
