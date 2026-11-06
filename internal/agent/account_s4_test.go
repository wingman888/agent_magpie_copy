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

// s4Isolate points HOME and the config dirs at an empty temporary directory
// and signs Codex in with ChatGPT mode.
func s4Isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	jwt := func(claims map[string]any) string {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
		b, _ := json.Marshal(claims)
		return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".x"
	}
	access := jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	id := jwt(map[string]any{"email": "me@example.com"})
	doc, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": id, "access_token": access, "refresh_token": "r", "account_id": "acc-1",
	}})
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestS4CodexOwnSignInLeftOutOfItsPicker(t *testing.T) {
	s4Isolate(t)

	for _, o := range viaDialFor("", "codex") {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Errorf("codex CLI picker still offers its own account: %s", o.Value)
		}
	}
	for _, m := range dialModelsFor("codex") {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Errorf("codex CLI catalog still offers its own account: %s", m.ID)
		}
	}

	found := false
	for _, o := range viaDial("") {
		if o.Value == "codex/gpt-5.5" {
			found = true
			if o.Note != "me@example.com · via dial" {
				t.Errorf("note = %q, want the signed-in user", o.Note)
			}
		}
	}
	if !found {
		t.Error("other agents should still see codex/gpt-5.5")
	}
}
