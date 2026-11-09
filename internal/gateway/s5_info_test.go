package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s5TEnv isolates HOME, config, cache and Claude's directory, and signs
// Codex and Copilot in. Claude Code is signed in only when claude is true.
func s5TEnv(t *testing.T, claude bool) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	s5TWrite(t, filepath.Join(root, "home", ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","tokens":{"access_token":"at-secret","refresh_token":"rt-secret","account_id":"acct-1"}}`)
	s5TWrite(t, filepath.Join(root, "config", "github-copilot", "apps.json"),
		`{"github.com:Iv1.abc":{"user":"octo","oauth_token":"gho_secret"}}`)
	if claude {
		s5TWrite(t, filepath.Join(root, "claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"sk-ant-secret-token"}}`)
	}
}

func s5TWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func s5TGet(t *testing.T, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestS5RootInfoNotesClaudeAndListsGemini(t *testing.T) {
	s5TEnv(t, true)
	body := s5TGet(t, "/")
	var info map[string]any
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatal(err)
	}
	notes, _ := info["notes"].([]any)
	if len(notes) != 1 || !strings.Contains(notes[0].(string), "但不作为 Provider 提供") {
		t.Errorf("claude note missing from root info: %s", body)
	}
	if strings.Contains(body, "sk-ant-secret") || strings.Contains(body, "at-secret") {
		t.Errorf("credentials leaked into root info: %s", body)
	}
	if !strings.Contains(body, "v1beta") {
		t.Errorf("gemini apis missing from root info: %s", body)
	}
}

func TestS5RootInfoHasNoNoteWithoutClaude(t *testing.T) {
	s5TEnv(t, false)
	if body := s5TGet(t, "/"); strings.Contains(body, `"notes"`) {
		t.Errorf("unexpected notes: %s", body)
	}
}

func TestS5ModelsListAccountsAndNotClaude(t *testing.T) {
	s5TEnv(t, true)
	body := s5TGet(t, "/v1/models")
	if !strings.Contains(body, `"id":"codex/`) {
		t.Errorf("codex models missing: %s", body)
	}
	if !strings.Contains(body, `"id":"copilot/`) {
		t.Errorf("copilot models missing: %s", body)
	}
	if strings.Contains(body, `"owned_by":"claude"`) || strings.Contains(body, `"id":"claude/`) {
		t.Errorf("claude offered in /v1/models: %s", body)
	}
	if strings.Contains(body, `"id":"copilot/auto"`) {
		t.Errorf("copilot auto must not be listed: %s", body)
	}
}
