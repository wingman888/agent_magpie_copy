package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

func signIn(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, "none"))
	os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
	os.WriteFile(filepath.Join(h, ".codex", "auth.json"), []byte(`{"tokens":{"access_token":"a","account_id":"x"}}`), 0o600)
	os.MkdirAll(filepath.Join(h, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(h, ".config", "github-copilot", "apps.json"), []byte(`{"github.com:app":{"user":"octocat","oauth_token":"gho"}}`), 0o600)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return h
}

func values(opts []Option) map[string]Option {
	m := map[string]Option{}
	for _, o := range opts {
		m[o.Value] = o
	}
	return m
}

func TestCodexSkipsItsOwnAccount(t *testing.T) {
	h := signIn(t)
	a := codex(h)
	vals := values(a.Fields[0].Options(nil))
	for v := range vals {
		if strings.HasPrefix(v, "codex/") {
			t.Errorf("codex picker offers %s", v)
		}
	}
	if _, ok := vals["copilot/claude-sonnet-4.5"]; !ok {
		t.Errorf("copilot missing: %v", vals)
	}
	if _, ok := vals["relay/m1"]; !ok {
		t.Errorf("relay missing: %v", vals)
	}
	for _, m := range dialModelsFor("codex") {
		if strings.HasPrefix(m.ID, "codex/") {
			t.Errorf("codex catalog has %s", m.ID)
		}
	}
	// routing Codex through dial writes a catalog without its own account
	if err := a.Fields[0].Set("relay/m1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(h, ".codex", "dial-models.json"))
	var cat struct {
		Models []struct {
			Slug string `json:"slug"`
		} `json:"models"`
	}
	json.Unmarshal(b, &cat)
	if len(cat.Models) == 0 {
		t.Fatalf("catalog: %s", b)
	}
	for _, m := range cat.Models {
		if strings.HasPrefix(m.Slug, "codex/") {
			t.Errorf("catalog file has %s", m.Slug)
		}
	}
}

func TestOtherAgentsSeeAccounts(t *testing.T) {
	h := signIn(t)
	vals := values(claude(h).Fields[0].Options(nil))
	o, ok := vals["codex/gpt-5.5"]
	if !ok {
		t.Fatalf("claude misses codex: %v", vals)
	}
	if !strings.Contains(o.Note, "ChatGPT") || strings.Contains(o.Note, "Codex ·") {
		t.Errorf("note: %q", o.Note)
	}
	if c := vals["copilot/claude-sonnet-4.5"]; !strings.Contains(c.Note, "octocat") {
		t.Errorf("copilot note: %q", c.Note)
	}
	if r := vals["relay/m1"]; !strings.Contains(r.Note, "Relay") {
		t.Errorf("relay note: %q", r.Note)
	}
	found := false
	for _, m := range dialModels() {
		if m.ID == "codex/gpt-5.5" {
			found = true
		}
	}
	if !found {
		t.Error("dialModels misses codex")
	}
}
