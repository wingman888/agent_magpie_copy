package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// iconcliIsolate points magpie's config, cache and the agents' homes at a
// temp dir so the test never touches the real ones.
func iconcliIsolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

func TestIconcliPresetCannotSetIcon(t *testing.T) {
	iconcliIsolate(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	err = setIcon("deepseek", "openai")
	if err == nil || !strings.Contains(err.Error(), "only custom providers") {
		t.Fatalf("setIcon on a preset: err = %v", err)
	}

	p2, _ := provider.FromPreset("deepseek")
	if err := applyPairs(&p2, []string{"icon=openai"}); err == nil || !strings.Contains(err.Error(), "only custom providers") {
		t.Fatalf("icon= on a preset add: err = %v", err)
	}
}

func TestIconcliCustomIconFromFile(t *testing.T) {
	iconcliIsolate(t)
	if err := provider.Save(provider.Provider{ID: "iconcli-own", Name: "Own", Key: "sk",
		Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "logo.svg")
	if err := os.WriteFile(img, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := setIcon("iconcli-own", img); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("iconcli-own")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".svg") {
		t.Fatalf("icon = %q, want file:<name>.svg", p.Icon)
	}
	if _, err := os.Stat(provider.IconFile(strings.TrimPrefix(p.Icon, "file:"))); err != nil {
		t.Fatalf("stored icon missing: %v", err)
	}

	// "" goes back to the default icon
	if err := setIcon("iconcli-own", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("iconcli-own"); p.Icon != "" {
		t.Fatalf("icon after reset = %q", p.Icon)
	}
}

func TestIconcliArgForms(t *testing.T) {
	iconcliIsolate(t)
	if got, err := iconArg("openai"); err != nil || got != "openai" {
		t.Fatalf("built-in name: %q, %v", got, err)
	}
	if got, err := iconArg(""); err != nil || got != "" {
		t.Fatalf("empty: %q, %v", got, err)
	}
	if _, err := iconArg("missing/logo.png"); err == nil {
		t.Fatal("a missing image path was accepted")
	}
	txt := filepath.Join(t.TempDir(), "note.png")
	os.WriteFile(txt, []byte("not really a picture"), 0o644)
	if _, err := iconArg(txt); err == nil {
		t.Fatal("a text file was accepted as an icon")
	}
}

func TestIconcliHeaderPairs(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://relay.example.com/v1", "key=sk",
		"header.X-Org-Id=acme", "HEADER.x-trace=on"}); err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" || p.Headers["x-trace"] != "on" || len(p.Headers) != 2 {
		t.Fatalf("headers = %v", p.Headers)
	}
}
