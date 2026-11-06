package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func pcliIsolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

func pcliWritePNG(t *testing.T, path string) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	img.Set(1, 1, color.RGBA{0, 128, 255, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPairsHeaders(t *testing.T) {
	pcliIsolate(t)
	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://x.example.com/v1", "header.X-Org-Id=acme", "HEADER.x-trace=a=b"}); err != nil {
		t.Fatal(err)
	}
	if p.Chat != "https://x.example.com/v1" {
		t.Errorf("url not applied: %q", p.Chat)
	}
	if p.Headers["X-Org-Id"] != "acme" {
		t.Errorf("header name case lost: %#v", p.Headers)
	}
	if p.Headers["x-trace"] != "a=b" {
		t.Errorf("header value with '=' or lower-case name: %#v", p.Headers)
	}
}

func TestApplyPairsIconRejectedForPreset(t *testing.T) {
	pcliIsolate(t)
	p, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	err = applyPairs(&p, []string{"icon=openai"})
	if err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("preset icon error = %v", err)
	}
}

func TestResolveIcon(t *testing.T) {
	pcliIsolate(t)
	if got, err := resolveIcon("openai"); err != nil || got != "openai" {
		t.Errorf("builtin: %q, %v", got, err)
	}
	if got, err := resolveIcon(""); err != nil || got != "" {
		t.Errorf("clear: %q, %v", got, err)
	}
	dir := t.TempDir()
	img := filepath.Join(dir, "logo.png")
	pcliWritePNG(t, img)
	got, err := resolveIcon(img)
	if err != nil || !strings.HasPrefix(got, "file:") || !strings.HasSuffix(got, ".png") {
		t.Fatalf("png file: %q, %v", got, err)
	}
	txt := filepath.Join(dir, "notes.txt")
	os.WriteFile(txt, []byte("hello, plain text"), 0o644)
	if _, err := resolveIcon(txt); err == nil {
		t.Error("plain text accepted as an icon")
	}
	if _, err := resolveIcon(filepath.Join(dir, "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestIconCommandOnCustomAndPreset(t *testing.T) {
	pcliIsolate(t)
	preset, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	preset.Key = "sk-test"
	if err := provider.Save(preset); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"magpie", "icon", "deepseek", "openai"}); err == nil ||
		!strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("preset icon command error = %v", err)
	}

	if err := provider.Save(provider.Provider{Name: "My Relay", Chat: "https://relay.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	img := filepath.Join(t.TempDir(), "logo.png")
	pcliWritePNG(t, img)
	if err := providerCmd([]string{"magpie", "icon", "my-relay", img}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") {
		t.Fatalf("icon = %q", p.Icon)
	}
	if provider.IconFile(strings.TrimPrefix(p.Icon, "file:")) == "" {
		t.Fatal("stored icon does not resolve")
	}
	if err := providerCmd([]string{"magpie", "icon", "my-relay", ""}); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("my-relay"); p.Icon != "" {
		t.Fatalf("icon not cleared: %q", p.Icon)
	}
}

func TestFallbackCommandSetsAndCleans(t *testing.T) {
	pcliIsolate(t)
	if err := provider.Save(provider.Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"magpie", "fallback", "relay", "openai/gpt-5.5", " ", "openai/gpt-5.5", "deepseek/deepseek-chat "}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Fallback, "|") != "openai/gpt-5.5|deepseek/deepseek-chat" {
		t.Fatalf("fallback = %q", p.Fallback)
	}
	if err := providerCmd([]string{"magpie", "fallback", "relay", "-"}); err != nil {
		t.Fatal(err)
	}
	if p, _ = provider.Find("relay"); len(p.Fallback) != 0 {
		t.Fatalf("fallback not cleared: %q", p.Fallback)
	}
}
