package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s5Login writes a ChatGPT-mode Codex sign-in under home.
func s5Login(t *testing.T, home string) {
	t.Helper()
	doc := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"access_token": "tok-s5", "account_id": "acc-s5",
	}}
	b, _ := json.Marshal(doc)
	path := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestS5SaveWithAddressStillSavesOnlyAccountModels: saving a Codex account
// with an address and key must not store either, and must not hide the
// account from All().
func TestS5SaveWithAddressStillSavesOnlyAccountModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	s5Login(t, home)

	err := Save(Provider{
		ID:     "codex",
		Name:   "Codex",
		Chat:   "https://relay.s5.example/v1",
		Key:    "sk-s5-secret",
		Models: []string{"gpt-5.5"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"relay.s5.example", "sk-s5-secret"} {
		if strings.Contains(string(raw), bad) {
			t.Errorf("config file holds %q:\n%s", bad, raw)
		}
	}

	var p *Provider
	ps := All()
	for i := range ps {
		if ps[i].ID == "codex" {
			p = &ps[i]
		}
	}
	if p == nil || p.Account == nil || p.Account.Agent != "codex" {
		t.Fatalf("codex account hidden from All(): %+v", ps)
	}
	if p.Chat != "" || p.Key != "" || !p.Ready() {
		t.Errorf("account provider carries address or key, or is not ready: %+v", p)
	}
	got := p.Exposed()
	if len(got) != 1 || got[0].ID != "gpt-5.5" {
		t.Errorf("exposed models = %v, want only gpt-5.5", got)
	}
}
