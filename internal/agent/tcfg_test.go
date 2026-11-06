package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tcfgAgentEnv isolates HOME and XDG dirs for one test.
func tcfgAgentEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func tcfgAgentWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTcfgCodexCLIDoesNotListItsOwnLogin(t *testing.T) {
	home := tcfgAgentEnv(t)

	claims, _ := json.Marshal(map[string]any{"email": "dev@example.com"})
	idTok := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	auth, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"id_token": idTok, "access_token": "tok-tcfg-agent", "account_id": "acct-tcfg"},
	})
	tcfgAgentWrite(t, filepath.Join(home, ".codex", "auth.json"), string(auth))
	tcfgAgentWrite(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "dial", "providers.json"),
		`{"providers":[{"id":"acme","name":"Acme","chat":"https://acme.example.com/v1","key":"sk-tcfg","models":["big-1"]}]}`)

	own := codexDialModels()
	for _, m := range own {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Fatalf("Codex CLI's own list must not hold codex/ models, got %s", m.ID)
		}
	}
	found := false
	for _, m := range own {
		if m.ID == "acme/big-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Codex CLI's list should still hold other providers' models: %v", own)
	}

	all := dialModels()
	hasCodex := false
	for _, m := range all {
		if m.ID == "codex/gpt-5.5" {
			hasCodex = true
			if !strings.Contains(m.Name, "dev@example.com") {
				t.Fatalf("account model should be annotated with the user, got %q", m.Name)
			}
			if strings.Contains(m.Name, "Codex") {
				t.Fatalf("account model should not be annotated with the provider name, got %q", m.Name)
			}
		}
	}
	if !hasCodex {
		t.Fatal("other agents' lists should include codex/gpt-5.5")
	}
}
