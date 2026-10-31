package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexListSkipsOwnAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	b, _ := json.Marshal(map[string]any{"tokens": map[string]any{"access_token": "a"}})
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600)
	os.MkdirAll(filepath.Join(home, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "github-copilot", "apps.json"), []byte(`{"github.com:x":{"user":"u","oauth_token":"t"}}`), 0o600)
	for _, o := range viaDialFor("codex", "") {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Fatal("own account listed", o.Value)
		}
	}
	var cop, cod bool
	for _, o := range viaDial("") {
		cod = cod || strings.HasPrefix(o.Value, "codex/")
		cop = cop || strings.HasPrefix(o.Value, "copilot/")
	}
	if !cod || !cop {
		t.Fatal("other agents should see both")
	}
	for _, o := range viaDialFor("codex", "") {
		if strings.HasPrefix(o.Value, "copilot/") && !strings.HasPrefix(o.Note, "u ") {
			t.Fatalf("note %q", o.Note)
		}
	}
}
