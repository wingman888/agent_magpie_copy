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

// icnCLIPNG is a valid 1x1 PNG.
func icnCLIPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 9, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIcnCLIApplyPairsIconName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var p provider.Provider
	if err := applyPairs(&p, []string{"icon=openai"}); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "openai" {
		t.Errorf("Icon = %q, want openai", p.Icon)
	}
	if err := applyPairs(&p, []string{"icon=../evil"}); err == nil {
		t.Error("a path-like icon name must be rejected")
	}
}

func TestIcnCLIApplyPairsIconFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	src := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(src, icnCLIPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	var p provider.Provider
	if err := applyPairs(&p, []string{"icon=" + src}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Fatalf("Icon = %q, want a file: reference to a stored png", p.Icon)
	}
}

func TestIcnCLIPresetRefusesIcon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := addProvider([]string{"deepseek", "sk-icn-cli", "icon=openai"}); err == nil {
		t.Error("provider add of a preset with icon= must be refused")
	}
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-icn-cli"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"magpie", "icon", "deepseek", "openai"}); err == nil || !strings.Contains(err.Error(), "only custom providers") {
		t.Errorf("icon on a preset: err = %v, want a clear refusal", err)
	}
}

func TestIcnCLIIconCommandCustom(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "icn-cli", Name: "Icn CLI", Chat: "https://relay.example.com/v1", Key: "sk-icn-cli2"}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(src, icnCLIPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	out := hdrCLIStdout(t, func() error { return providerCmd([]string{"magpie", "icon", "icn-cli", src}) })
	if !strings.Contains(out, "file:") {
		t.Errorf("output does not show the icon reference:\n%s", out)
	}
	p, err := provider.Find("icn-cli")
	if err != nil || !strings.HasPrefix(p.Icon, "file:") {
		t.Fatalf("icon not saved: %q (err %v)", p.Icon, err)
	}
	if err := providerCmd([]string{"magpie", "icon", "icn-cli", ""}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("icn-cli"); p.Icon != "" {
		t.Errorf("icon after \"\" = %q, want generic", p.Icon)
	}
	src2 := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(src2, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"magpie", "icon", "icn-cli", src2}); err == nil {
		t.Error("a plain text file must be refused as an icon")
	}
}
