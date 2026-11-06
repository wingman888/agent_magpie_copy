package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func isolateCLI(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestPresetCannotTakeAnIcon(t *testing.T) {
	isolateCLI(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	err = iconCmd([]string{"deepseek", "logo.png"})
	if err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("icon on a preset: err = %v", err)
	}
	var p provider.Provider = pr
	if err := applyPairs(&p, []string{"icon=openai"}); err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("icon= on a preset: err = %v", err)
	}
}

func TestIconFileOnCustomProvider(t *testing.T) {
	isolateCLI(t)
	img := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(img, append([]byte("\x89PNG\r\n\x1a\n"), []byte("logo")...), 0o644); err != nil {
		t.Fatal(err)
	}
	var p provider.Provider
	if err := applyPairs(&p, []string{"icon=" + img}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Fatalf("icon = %q", p.Icon)
	}
	if err := applyPairs(&p, []string{"icon=" + filepath.Join(t.TempDir(), "missing.png")}); err == nil {
		t.Fatal("a missing icon file was accepted")
	}
	text := filepath.Join(t.TempDir(), "notes.png")
	if err := os.WriteFile(text, []byte("plain text, not a picture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyPairs(&p, []string{"icon=" + text}); err == nil {
		t.Fatal("plain text accepted as an icon")
	}
}

func TestHeaderPairsKeepTheirCase(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://relay.example/v1", "header.X-Org-Id=acme", "HEADER.x-trace=1"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "x-trace": "1"}
	if !reflect.DeepEqual(p.Headers, want) {
		t.Fatalf("headers = %#v, want %#v", p.Headers, want)
	}
	if err := applyPairs(&p, []string{"header.Bad Name=x"}); err == nil {
		t.Error("a header name with a space was accepted")
	}
}

func TestFallbackCommandSetsAndClears(t *testing.T) {
	isolateCLI(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := fallbackCmd([]string{"relay", "openai/gpt-5.5", "openai/gpt-5.5", "", "deepseek/deepseek-chat"}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"openai/gpt-5.5", "deepseek/deepseek-chat"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
	if err := fallbackCmd([]string{"relay", "-"}); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("relay"); len(p.Fallback) != 0 {
		t.Fatalf("fallback not cleared: %q", p.Fallback)
	}
}
