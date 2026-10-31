package provider_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func has(id string) bool {
	for _, p := range provider.All() {
		if p.ID == id {
			return true
		}
	}
	return false
}

func TestAcceptRemoveAndRestoreCodexAccount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PATH", t.TempDir()) // no claude, no fallbacks under this home
	exp := float64(time.Now().Add(time.Hour).Unix())
	auth := filepath.Join(home, ".codex", "auth.json")
	writeJSON(t, auth, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  jwt(map[string]any{"exp": exp}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
	writeJSON(t, filepath.Join(home, ".codex", "models_cache.json"), map[string]any{
		"models": []any{
			map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1},
			map[string]any{"slug": "gpt-5.4", "display_name": "GPT-5.4", "visibility": "list", "priority": 2},
		},
	})
	before, _ := os.ReadFile(auth)

	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if !has("codex") {
		t.Fatal("signed-in codex is not a provider")
	}
	if _, _, ok := provider.Resolve("codex/gpt-5.5"); !ok {
		t.Fatal("codex/gpt-5.5 does not resolve")
	}

	if err := provider.Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if has("codex") {
		t.Error("removed account still listed")
	}
	if _, _, ok := provider.Resolve("codex/gpt-5.5"); ok {
		t.Error("removed account still resolves")
	}
	for _, e := range provider.Catalog() {
		if e.Provider.ID == "codex" {
			t.Errorf("removed account still in the catalog: %s", e.ID)
		}
	}
	x := provider.Excluded()
	if len(x) != 1 || x[0].Provider != "codex" || x[0].Agent != "codex" || x[0].Why == "" {
		t.Fatalf("excluded: %+v", x)
	}
	if after, _ := os.ReadFile(auth); !bytes.Equal(before, after) {
		t.Error("the agent's own login was touched")
	}

	// adding it back restores it, with the earlier picks
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	ex := p.Exposed()
	if len(provider.Excluded()) != 0 || len(ex) != 1 || ex[0].ID != "gpt-5.5" {
		t.Fatalf("after restore: %+v %+v", provider.Excluded(), ex)
	}
	if _, _, ok := provider.Resolve("codex/gpt-5.5"); !ok {
		t.Error("restored codex does not resolve")
	}
}

func TestAcceptClaudeSignedOutIsNotAProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	writeJSON(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-x", "refreshToken": "sk-ant-ort01-x",
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max",
			"scopes": []string{"user:inference", "user:profile"},
		},
	})
	// Claude Code says nobody is signed in (and exits 1, as it does)
	script := "#!/bin/sh\necho '{\"loggedIn\": false, \"authMethod\": \"none\"}'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if has("claude") {
		t.Fatal("signed-out Claude Code is listed as a provider")
	}
}
