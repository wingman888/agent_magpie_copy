package agent_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/dial/internal/agent"
	"github.com/yetone/dial/internal/provider"
)

func jwt(m map[string]any) string {
	b, _ := json.Marshal(m)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
}

func options(t *testing.T, id string) []agent.Option {
	t.Helper()
	a, err := agent.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	f := a.Fields[0]
	return f.Options(a.Values())
}

func TestAcceptAccountModelsInAgentPickers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	auth := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"access_token":%q,"refresh_token":"r","account_id":"acct-1"}}`,
		jwt(map[string]any{"email": "me@example.com"}), jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(auth), 0o600)
	if err := provider.Save(provider.Provider{ID: "mine", Name: "Mine", Key: "k", Chat: "https://x.example/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}

	var seenCodexModel, seenMine bool
	for _, o := range options(t, "codex") {
		if strings.HasPrefix(o.Value, "codex/") || strings.HasPrefix(o.Value, "dial/codex/") {
			t.Fatalf("codex's own account offered back to itself: %+v", o)
		}
		if strings.HasSuffix(o.Value, "mine/m1") {
			seenMine = true
		}
	}
	if !seenMine {
		t.Fatal("ordinary provider missing from codex's list")
	}
	for _, o := range options(t, "claude") {
		if o.Value == "codex/gpt-5.5" {
			seenCodexModel = true
			if !strings.Contains(o.Note, "me@example.com") {
				t.Fatalf("note should name the signed-in user: %+v", o)
			}
		}
	}
	if !seenCodexModel {
		t.Fatal("claude's list lacks codex/gpt-5.5")
	}
}
