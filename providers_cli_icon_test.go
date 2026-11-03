package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func isolateCLI(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	dir := t.TempDir()
	return dir
}

func writePNG(t *testing.T, dir, name, payload string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, append([]byte("\x89PNG\r\n\x1a\n"), []byte(payload)...), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProviderIconCustomSetAndClear(t *testing.T) {
	dir := isolateCLI(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	img := writePNG(t, dir, "logo.png", "logo")

	if err := providerCmd([]string{"magpie", "icon", "relay", img}); err != nil {
		t.Fatalf("set icon: %v", err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || provider.IconFile(strings.TrimPrefix(p.Icon, "file:")) == "" {
		t.Fatalf("icon = %q", p.Icon)
	}

	if err := providerCmd([]string{"magpie", "icon", "relay", "openai"}); err != nil {
		t.Fatalf("built-in name: %v", err)
	}
	if p, _ = provider.Find("relay"); p.Icon != "openai" {
		t.Fatalf("icon = %q, want openai", p.Icon)
	}

	if err := providerCmd([]string{"magpie", "icon", "relay", ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if p, _ = provider.Find("relay"); p.Icon != "" {
		t.Fatalf("icon = %q after clearing", p.Icon)
	}
}

func TestProviderIconRejectsPresetAndText(t *testing.T) {
	dir := isolateCLI(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	err := providerCmd([]string{"magpie", "icon", "deepseek", "openai"})
	if err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("preset icon error = %v", err)
	}

	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "notes.txt")
	os.WriteFile(txt, []byte("not a picture"), 0o644)
	if err := providerCmd([]string{"magpie", "icon", "relay", txt}); err == nil {
		t.Fatal("text file accepted as icon")
	}
}

func TestAddProviderIconRules(t *testing.T) {
	isolateCLI(t)
	if err := addProvider([]string{"deepseek", "sk-1", "icon=openai"}); err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("preset with icon= = %v", err)
	}
	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://relay.example/v1", "icon=openai"}); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "openai" {
		t.Fatalf("icon = %q", p.Icon)
	}
	if err := applyPairs(&p, []string{"icon=no/such/file.png"}); err == nil {
		t.Fatal("missing icon file accepted")
	}
}
