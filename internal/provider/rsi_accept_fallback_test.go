package provider_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func jwt(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func put(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func home(t *testing.T) string {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	return h
}

func TestAcceptFallbackIsCleanedAndKept(t *testing.T) {
	home(t)
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "https://plan.example/v1",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("plan")
	if err != nil || !slices.Equal(p.Fallback, []string{"codex/gpt-5.5", "other/m"}) {
		t.Fatalf("%+v %v", p, err)
	}
	found := false
	for _, q := range provider.All() {
		if q.ID == "plan" {
			found = slices.Equal(q.Fallback, []string{"codex/gpt-5.5", "other/m"})
		}
	}
	if !found {
		t.Fatal("All lost the fallback")
	}
	// clearing
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "https://plan.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("plan"); len(p.Fallback) != 0 {
		t.Fatalf("not cleared: %v", p.Fallback)
	}
}

func TestAcceptAccountKeepsFallback(t *testing.T) {
	h := home(t)
	exp := float64(time.Now().Add(time.Hour).Unix())
	put(t, filepath.Join(h, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      jwt(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  jwt(map[string]any{"exp": exp}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
	put(t, filepath.Join(h, ".codex", "models_cache.json"), map[string]any{
		"models": []any{map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1}},
	})
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Key: "k", Chat: "https://plan.example/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "codex", Fallback: []string{"plan/m1"}}); err != nil {
		t.Fatal(err)
	}
	a, err := provider.Find("codex")
	if err != nil || a.Account == nil || !slices.Equal(a.Fallback, []string{"plan/m1"}) {
		t.Fatalf("%+v %v", a, err)
	}
	ok := false
	for _, p := range provider.All() {
		if p.ID == "codex" && p.Account != nil && slices.Equal(p.Fallback, []string{"plan/m1"}) {
			ok = true
		}
	}
	if !ok {
		t.Fatal("All lost the account fallback")
	}
}

func TestAcceptOldConfigWithoutFallbackStillLoads(t *testing.T) {
	h := home(t)
	dir := filepath.Join(h, ".config", "magpie")
	if err := provider.Save(provider.Provider{ID: "old", Name: "Old", Key: "k", Chat: "https://old.example/v1", Models: []string{"m"}}); err != nil {
		t.Fatal(err)
	}
	_ = dir
	p, err := provider.Find("old")
	if err != nil || len(p.Fallback) != 0 || p.Name != "Old" {
		t.Fatalf("%+v %v", p, err)
	}
}
