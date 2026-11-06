package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// accountSandbox points every location agents and dial read at a fresh tree.
func accountSandbox(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	return home
}

// codexSignIn writes a ChatGPT-mode Codex login under home.
func codexSignIn(t *testing.T, home string) {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"email": "dev@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	idToken := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	body, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"id_token": idToken, "access_token": "tok", "account_id": "acct"},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// modelOptions is the model picker an agent offers.
func modelOptions(t *testing.T, a *Agent) []Option {
	t.Helper()
	for _, f := range a.Fields {
		if f.Key == "model" {
			return f.Options(map[string]string{})
		}
	}
	t.Fatalf("%s has no model field", a.ID)
	return nil
}

func TestCodexCLIPickerLeavesOutItsOwnAccount(t *testing.T) {
	home := accountSandbox(t)
	codexSignIn(t, home)

	for _, o := range modelOptions(t, codex(home)) {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Errorf("Codex CLI's own picker offers %s", o.Value)
		}
	}
	found := false
	for _, o := range modelOptions(t, gemini(home)) {
		if o.Value == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Error("another agent's picker lacks codex/gpt-5.5")
	}
}

func TestAccountModelNoteNamesTheUser(t *testing.T) {
	home := accountSandbox(t)
	codexSignIn(t, home)

	for _, o := range modelOptions(t, gemini(home)) {
		if o.Value != "codex/gpt-5.5" {
			continue
		}
		if !strings.HasPrefix(o.Note, "dev@example.com") {
			t.Errorf("note = %q, want the signed-in user first", o.Note)
		}
		if strings.Contains(o.Note, "Codex") {
			t.Errorf("note %q names the provider instead of the user", o.Note)
		}
		return
	}
	t.Fatal("codex/gpt-5.5 not in the picker")
}
