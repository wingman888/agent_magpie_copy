package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolateIcons(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

var pngData = append([]byte("\x89PNG\r\n\x1a\n"), []byte("body-of-png")...)

func TestStoreIconReferenceAndDedup(t *testing.T) {
	isolateIcons(t)
	ref, err := StoreIcon(pngData)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") || len(ref) != len("file:")+16+len(".png") {
		t.Fatalf("bad ref %q", ref)
	}
	ref2, err := StoreIcon(pngData)
	if err != nil || ref2 != ref {
		t.Fatalf("second store = %q, %v; want %q", ref2, err, ref)
	}
	entries, _ := os.ReadDir(iconsDir())
	if len(entries) != 1 {
		t.Fatalf("icons dir has %d files, want 1", len(entries))
	}
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	if path == "" {
		t.Fatal("IconFile returned empty for stored icon")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, pngData) {
		t.Fatalf("stored content mismatch: %v", err)
	}
}

func TestStoreIconTypes(t *testing.T) {
	isolateIcons(t)
	cases := map[string][]byte{
		".jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 1, 2},
		".gif":  []byte("GIF89a...."),
		".webp": append([]byte("RIFF\x10\x00\x00\x00WEBP"), 1, 2, 3, 4),
		".ico":  {0x00, 0x00, 0x01, 0x00, 9, 9},
		".svg":  []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`),
	}
	for ext, data := range cases {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Errorf("%s: %v", ext, err)
			continue
		}
		if !strings.HasSuffix(ref, ext) {
			t.Errorf("%s: ref %q", ext, ref)
		}
	}
	// SVG is recognised by content, not extension.
	if _, err := StoreIcon([]byte("<svg></svg>")); err != nil {
		t.Errorf("svg text: %v", err)
	}
}

func TestStoreIconRejects(t *testing.T) {
	isolateIcons(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty accepted")
	}
	if _, err := StoreIcon([]byte("just some text")); err == nil {
		t.Error("plain text accepted")
	}
	big := make([]byte, MaxIcon+1)
	copy(big, pngData)
	if _, err := StoreIcon(big); err == nil || !strings.Contains(err.Error(), "smaller") {
		t.Errorf("oversize error = %v", err)
	}
	exact := make([]byte, MaxIcon)
	copy(exact, pngData)
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("exact MaxIcon rejected: %v", err)
	}
}

func TestStoreIconFile(t *testing.T) {
	home := isolateIcons(t)
	src := filepath.Join(home, "logo.png")
	os.WriteFile(src, pngData, 0o644)
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(src)
	if IconFile(strings.TrimPrefix(ref, "file:")) == "" {
		t.Error("stored icon gone after source removed")
	}
	if _, err := StoreIconFile(filepath.Join(home, "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestIconFileValidation(t *testing.T) {
	isolateIcons(t)
	ref, _ := StoreIcon(pngData)
	name := strings.TrimPrefix(ref, "file:")
	if IconFile(name) == "" {
		t.Fatal("valid name not found")
	}
	for _, bad := range []string{"", "../providers.json", "x.png", "0123456789abcdef.exe", "0123456789ABCDEF.png", "../0123456789abcdef.png", "0123456789abcdef/x.png"} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestPruneIcons(t *testing.T) {
	isolateIcons(t)
	old := time.Now().Add(-2 * time.Hour)

	stale, _ := StoreIcon(pngData)
	staleName := strings.TrimPrefix(stale, "file:")
	fresh, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nfresh"))
	kept, _ := StoreIcon([]byte("\x89PNG\r\n\x1a\nreferenced"))
	keptName := strings.TrimPrefix(kept, "file:")
	os.Chtimes(filepath.Join(iconsDir(), staleName), old, old)
	os.Chtimes(filepath.Join(iconsDir(), keptName), old, old)

	if err := Save(Provider{ID: "r", Name: "R", Chat: "https://example.com/v1", Key: "k", Icon: kept}); err != nil {
		t.Fatal(err)
	}
	// Saving another provider triggers pruning.
	if err := Save(Provider{ID: "s", Name: "S", Chat: "https://example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if IconFile(staleName) != "" {
		t.Error("unreferenced old icon was not pruned")
	}
	if IconFile(keptName) == "" {
		t.Error("referenced old icon was pruned")
	}
	if IconFile(strings.TrimPrefix(fresh, "file:")) == "" {
		t.Error("fresh unreferenced icon was pruned")
	}
}
