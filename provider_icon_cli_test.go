package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// useHome points magpie's config at a fresh temp home for one test.
func useHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProviderIconCommandAcceptsPNGAndSVG(t *testing.T) {
	useHome(t)
	if err := provider.Save(provider.Provider{ID: "gw", Name: "GW", Key: "sk-1", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte(strings.Repeat("p", 64))...)
	pngPath := writeTemp(t, "logo.png", png)
	if err := providerCmd([]string{"provider", "icon", "gw", pngPath}); err != nil {
		t.Fatalf("png: %v", err)
	}
	p, err := provider.Find("gw")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Fatalf("icon = %q, want file:<name>.png", p.Icon)
	}
	if provider.IconFile(strings.TrimPrefix(p.Icon, "file:")) == "" {
		t.Fatalf("icon %q does not resolve to a file", p.Icon)
	}

	svgPath := writeTemp(t, "mark.svg", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`))
	if err := providerCmd([]string{"provider", "icon", "gw", svgPath}); err != nil {
		t.Fatalf("svg: %v", err)
	}
	p, _ = provider.Find("gw")
	if !strings.HasSuffix(p.Icon, ".svg") {
		t.Fatalf("svg icon = %q", p.Icon)
	}

	// the same picture again is stored once
	dir := filepath.Dir(provider.IconFile("0123456789abcdef.png"))
	before, _ := os.ReadDir(dir)
	if err := providerCmd([]string{"provider", "icon", "gw", pngPath}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(dir)
	if len(before) != 2 || len(after) != 2 {
		t.Fatalf("icons folder holds %d files before and %d after re-setting the same png; want 2 and 2", len(before), len(after))
	}
}

func TestProviderIconCommandRejects(t *testing.T) {
	useHome(t)
	if err := provider.Save(provider.Provider{ID: "gw", Name: "GW", Key: "sk-1", Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "openai", Preset: "openai", Key: "sk-2"}); err != nil {
		t.Fatal(err)
	}
	png := writeTemp(t, "ok.png", append([]byte("\x89PNG\r\n\x1a\n"), []byte("x")...))
	if err := providerCmd([]string{"provider", "icon", "openai", png}); err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("preset icon error = %v, want the custom-only message", err)
	}
	text := writeTemp(t, "note.svg", []byte("just some plain text"))
	if err := providerCmd([]string{"provider", "icon", "gw", text}); err == nil {
		t.Fatal("plain text accepted as an icon")
	}
	big := writeTemp(t, "big.png", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, provider.MaxIcon)...))
	if err := providerCmd([]string{"provider", "icon", "gw", big}); err == nil {
		t.Fatal("icon over 1 MB accepted")
	}
	if err := providerCmd([]string{"provider", "icon", "gw", ""}); err != nil {
		t.Fatalf("empty value should reset the icon: %v", err)
	}
}
