package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// s6Isolate points HOME and config dirs at a fresh temporary directory.
func s6Isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	return home
}

func s6JWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// s6SignIn signs both Codex (ChatGPT mode) and Copilot in.
func s6SignIn(t *testing.T, home string) {
	t.Helper()
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      s6JWT(map[string]any{"email": "amy@example.com"}),
		"access_token":  s6JWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "ref-s6",
		"account_id":    "acct-s6",
	}}
	raw, _ := json.Marshal(doc)
	codexDir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cpDir := filepath.Join(home, "xdg", "github-copilot")
	if err := os.MkdirAll(cpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	apps := `{"github.com:s6":{"user":"octo","oauth_token":"gho_s6_a10"}}`
	if err := os.WriteFile(filepath.Join(cpDir, "apps.json"), []byte(apps), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A10: the Codex CLI's own list leaves out codex/… models; other agents keep them.
func TestS6A10CodexCLISkipsOwnAccountModels(t *testing.T) {
	home := s6Isolate(t)
	s6SignIn(t, home)

	var sawCopilot bool
	for _, o := range viaDialFor("", "codex") {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Errorf("Codex CLI picker offers its own model %s", o.Value)
		}
		if strings.HasPrefix(o.Value, "copilot/") {
			sawCopilot = true
		}
	}
	if !sawCopilot {
		t.Errorf("Codex CLI picker should still offer copilot/… models")
	}

	var sawCodex bool
	for _, o := range viaDialFor("", "") {
		if o.Value == "codex/gpt-5.5" {
			sawCodex = true
		}
	}
	if !sawCodex {
		t.Errorf("other agents' picker should offer codex/gpt-5.5")
	}

	for _, m := range dialModelsFor("codex") {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Errorf("Codex CLI model file lists %s", m.ID)
		}
	}
	var fileHasCodex bool
	for _, m := range dialModelsFor("") {
		if m.ID == "codex/gpt-5.5" {
			fileHasCodex = true
		}
	}
	if !fileHasCodex {
		t.Errorf("unrestricted dial model list should include codex/gpt-5.5")
	}
}
