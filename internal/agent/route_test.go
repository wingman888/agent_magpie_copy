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

func signedInCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	tok := func(c map[string]any) string {
		b, _ := json.Marshal(c)
		return "x." + base64.RawURLEncoding.EncodeToString(b) + ".y"
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"access_token": tok(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"id_token":     tok(map[string]any{"email": "a@b.com"}), "refresh_token": "r", "account_id": "acc"}})
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), b, 0o600)
}

func TestCodexAgentOmitsOwnAccountModels(t *testing.T) {
	signedInCodexHome(t)
	has := func(a *Agent, suffix string) (found bool) {
		for _, o := range a.Fields[0].Options(map[string]string{}) {
			if strings.HasSuffix(o.Value, suffix) {
				found = true
			}
		}
		return
	}
	cx, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range cx.Fields[0].Options(map[string]string{}) {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Fatalf("codex list contains its own account model %q", o.Value)
		}
	}
	oc, err := Find("opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !has(oc, "codex/gpt-5.5") {
		t.Fatal("other agents should list codex/gpt-5.5")
	}
}
