package provider

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// icnSandbox points the config dir at a fresh temp dir and returns the icons folder.
func icnSandbox(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return iconsDir()
}

// icnPNG encodes a small PNG whose pixels depend on seed, so different seeds
// give different content.
func icnPNG(t *testing.T, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: seed, G: 128, B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const icnSVG = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`

func icnEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestIcnStoreAcceptsPNGAndSVG(t *testing.T) {
	dir := icnSandbox(t)
	ref, err := StoreIcon(icnPNG(t, 1))
	if err != nil {
		t.Fatalf("PNG rejected: %v", err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("PNG ref = %q, want file:<16 hex>.png", ref)
	}
	if path := IconFile(strings.TrimPrefix(ref, "file:")); path == "" {
		t.Fatalf("IconFile rejects the name StoreIcon returned: %q", ref)
	} else if _, err := os.Stat(path); err != nil {
		t.Fatalf("stored PNG missing: %v", err)
	}
	sref, err := StoreIcon([]byte(icnSVG))
	if err != nil {
		t.Fatalf("SVG rejected: %v", err)
	}
	if !strings.HasSuffix(sref, ".svg") {
		t.Fatalf("SVG ref = %q, want .svg", sref)
	}
	if got := icnEntries(t, dir); len(got) != 2 {
		t.Fatalf("icons dir = %v, want two files", got)
	}
}

func TestIcnStoreDetectsOtherFormats(t *testing.T) {
	icnSandbox(t)
	cases := map[string][]byte{
		".jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 0, 16},
		".gif":  []byte("GIF89a\x01\x00\x01\x00"),
		".webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
		".ico":  {0, 0, 1, 0, 1, 0},
	}
	for ext, data := range cases {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Fatalf("%s rejected: %v", ext, err)
		}
		if !strings.HasSuffix(ref, ext) {
			t.Errorf("ref = %q, want suffix %s", ref, ext)
		}
	}
}

func TestIcnStoreRejectsBadInput(t *testing.T) {
	icnSandbox(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty content must be rejected")
	}
	if _, err := StoreIcon([]byte("just some plain text, not an image")); err == nil {
		t.Error("plain text must be rejected")
	}
	big := append(icnPNG(t, 2), make([]byte, MaxIcon)...)
	if len(big) <= MaxIcon {
		t.Fatal("test setup: content should exceed MaxIcon")
	}
	if _, err := StoreIcon(big); err == nil {
		t.Error("content over 1 MB must be rejected")
	}
	exact := append(icnPNG(t, 3), make([]byte, MaxIcon-len(icnPNG(t, 3)))...)
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("content of exactly MaxIcon bytes must be accepted: %v", err)
	}
}

func TestIcnStoreSameContentOnce(t *testing.T) {
	dir := icnSandbox(t)
	data := icnPNG(t, 4)
	a, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same picture gave two refs: %q and %q", a, b)
	}
	if got := icnEntries(t, dir); len(got) != 1 {
		t.Fatalf("icons dir = %v, want exactly one file", got)
	}
}

func TestIcnIconFileRejectsIllegalNames(t *testing.T) {
	icnSandbox(t)
	bad := []string{
		"",
		"../0123456789abcdef.png",
		"..\\0123456789abcdef.png",
		"sub/0123456789abcdef.png",
		"0123456789abcdef.exe",
		"0123456789ABCDEF.png",
		"0123456789abcde.png",
		"0123456789abcdef0.png",
		"0123456789abcdef",
		"../../etc/passwd",
		"0123456789abcdef.png/..",
	}
	for _, n := range bad {
		if got := IconFile(n); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", n, got)
		}
	}
	good := "0123456789abcdef.png"
	got := IconFile(good)
	if got == "" || filepath.Base(got) != good || filepath.Dir(got) != iconsDir() {
		t.Errorf("IconFile(%q) = %q, want a path inside the icons folder", good, got)
	}
}

func TestIcnCheckIconRejectsTraversal(t *testing.T) {
	icnSandbox(t)
	if err := CheckIcon(""); err != nil {
		t.Errorf("generic icon must be fine: %v", err)
	}
	if err := CheckIcon("openai"); err != nil {
		t.Errorf("built-in name rejected: %v", err)
	}
	for _, v := range []string{"file:../../secret.png", "file:/etc/passwd", "../x", "a b", "file:0123456789abcdef.png"} {
		if err := CheckIcon(v); err == nil {
			t.Errorf("CheckIcon(%q) accepted", v)
		}
	}
}

func TestIcnPruneRemovesOldUnreferenced(t *testing.T) {
	dir := icnSandbox(t)
	oldRef, err := StoreIcon(icnPNG(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	keptRef, err := StoreIcon(icnPNG(t, 11))
	if err != nil {
		t.Fatal(err)
	}
	freshRef, err := StoreIcon(icnPNG(t, 12))
	if err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-3 * time.Hour)
	for _, ref := range []string{oldRef, keptRef} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), long, long); err != nil {
			t.Fatal(err)
		}
	}
	// keptRef is old but referenced by a saved provider; freshRef is
	// unreferenced but was written just now.
	if err := Save(Provider{ID: "icn-keep", Name: "Keep", Chat: "https://relay.example.com/v1", Key: "sk-icn-1", Icon: keptRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(oldRef, "file:"))); !os.IsNotExist(err) {
		t.Errorf("old unreferenced icon should be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(keptRef, "file:"))); err != nil {
		t.Errorf("referenced icon must be kept: %v", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(freshRef, "file:"))); err != nil {
		t.Errorf("icon younger than an hour must be kept: %v", err)
	}
	if got := icnEntries(t, dir); len(got) != 2 {
		t.Errorf("icons dir = %v, want 2 files left", got)
	}
}

func TestIcnSetIconCustomAndPreset(t *testing.T) {
	icnSandbox(t)
	if err := Save(Provider{ID: "icn-custom", Name: "Custom", Chat: "https://relay.example.com/v1", Key: "sk-icn-2"}); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIcon(icnPNG(t, 20))
	if err != nil {
		t.Fatal(err)
	}
	if err := SetIcon("icn-custom", ref); err != nil {
		t.Fatalf("custom provider icon: %v", err)
	}
	p, err := Find("icn-custom")
	if err != nil || p.Icon != ref {
		t.Fatalf("icon = %q (err %v), want %q", p.Icon, err, ref)
	}
	if err := SetIcon("icn-custom", "file:../../secret.png"); err == nil {
		t.Error("traversal reference must be rejected")
	}
	if err := SetIcon("icn-custom", ""); err != nil {
		t.Fatalf("clearing the icon: %v", err)
	}
	if p, _ := Find("icn-custom"); p.Icon != "" {
		t.Errorf("icon after clearing = %q, want empty", p.Icon)
	}

	pr, err := FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-icn-3"
	if err := Save(pr); err != nil {
		t.Fatal(err)
	}
	if err := SetIcon("deepseek", "openai"); err == nil {
		t.Error("a preset must refuse its own icon")
	}
}
