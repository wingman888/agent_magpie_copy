package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/catalog"
)

// exposeSandbox points credential, config and cache locations at a temp dir.
// It is self-contained so this file does not depend on other test files.
func exposeSandbox(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, "claude"))
	exposeResetState()
	oldCodex, oldToken := CodexBase, CopilotTokenURL
	t.Cleanup(func() {
		CodexBase, CopilotTokenURL = oldCodex, oldToken
		exposeResetState()
	})
}

// exposeResetState forgets cached Copilot sessions.
func exposeResetState() {
	sessionMu.Lock()
	sessionByOAuth = map[string]copilotSession{}
	sessionMu.Unlock()
}

// exposeSignInCopilot writes a GitHub entry into the sandbox's Copilot hosts.json.
func exposeSignInCopilot(t *testing.T, oauth string) {
	t.Helper()
	entries := map[string]any{
		"github.com:Iv1.test": map[string]any{"user": "octocat", "oauth_token": oauth},
	}
	b, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "hosts.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// exposeMustAccount returns the listed account provider with the given id.
func exposeMustAccount(t *testing.T, id string) Provider {
	t.Helper()
	for _, p := range Accounts() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("account %q not listed; accounts = %+v", id, Accounts())
	return Provider{}
}

// A borrowed login exposes every model it serves, even past the threshold
// that hides the list of a plain provider.
func TestAcctExposeAllModelsOverThreshold(t *testing.T) {
	exposeSandbox(t)
	exposeSignInCopilot(t, "gho_test")

	var ms []catalog.Model
	for i := 0; i < manyModels+6; i++ {
		ms = append(ms, catalog.Model{ID: fmt.Sprintf("model-%02d", i), Name: fmt.Sprintf("Model %02d", i)})
	}
	if err := catalog.SaveLive("copilot", "https://api.example.invalid", ms); err != nil {
		t.Fatal(err)
	}

	p := exposeMustAccount(t, "copilot")
	if got := len(p.Available()); got != len(ms) {
		t.Fatalf("Available() = %d models; want %d", got, len(ms))
	}
	if got := len(p.Exposed()); got != len(ms) {
		t.Fatalf("Exposed() = %d models; want all %d", got, len(ms))
	}
}
