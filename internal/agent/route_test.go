package agent

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/catalog"
)

func routeTestPut(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func routeFind(ms []catalog.Model, id string) (catalog.Model, bool) {
	for _, m := range ms {
		if m.ID == id {
			return m, true
		}
	}
	return catalog.Model{}, false
}

func routeHasPrefix(ms []catalog.Model, prefix string) bool {
	for _, m := range ms {
		if strings.HasPrefix(m.ID, prefix) {
			return true
		}
	}
	return false
}

// Codex's own account models stay out of Codex's own model file; every other
// agent's list still carries them, and account models are noted with the user.
func TestCodexListLeavesOutItsOwnAccountModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))

	payload := `{"email":"dev@example.com","https://api.openai.com/auth":{"chatgpt_plan_type":"pro","chatgpt_account_id":"acct-1"}}`
	id := "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
	routeTestPut(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"id_token":"`+id+`","access_token":"ACCESS","refresh_token":"R","account_id":"acct-1"}}`)
	routeTestPut(t, filepath.Join(home, "config", "github-copilot", "hosts.json"),
		`{"github.com":{"user":"octocat","oauth_token":"gho_x"}}`)

	own := dialModelsExcept("codex")
	if routeHasPrefix(own, "codex/") {
		t.Fatalf("Codex's own list must not contain codex/ models: %+v", own)
	}
	if !routeHasPrefix(own, "copilot/") {
		t.Fatalf("Codex's list should still carry other agents' account models: %+v", own)
	}
	if _, ok := routeFind(own, "copilot/auto"); ok {
		t.Fatal("copilot/auto must not be offered before a refresh")
	}

	all := dialModels()
	if !routeHasPrefix(all, "codex/") {
		t.Fatalf("other agents' lists must contain codex/ models: %+v", all)
	}
	if m, ok := routeFind(all, "codex/gpt-5.5"); !ok || !strings.HasSuffix(m.Name, " · dev@example.com") {
		t.Fatalf("codex model should be noted with the signed-in user, got %+v (found %v)", m, ok)
	}
	if m, ok := routeFind(own, "copilot/claude-sonnet-4.5"); !ok || !strings.HasSuffix(m.Name, " · octocat") {
		t.Fatalf("copilot model should be noted with the signed-in user, got %+v (found %v)", m, ok)
	}
}
