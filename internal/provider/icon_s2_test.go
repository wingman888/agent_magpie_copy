package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// iconTestEnv points the config dir (and so the icons dir) at a fresh temp dir.
func iconTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	return home
}

// iconTestPad returns data padded out to n bytes, keeping its prefix.
func iconTestPad(prefix []byte, n int) []byte {
	out := make([]byte, n)
	copy(out, prefix)
	return out
}

func iconTestPNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte("payload-png")...)
}

func iconTestSVG() []byte {
	return []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`)
}

func TestIconTestMagicTypes(t *testing.T) {
	iconTestEnv(t)
	webp := append([]byte("RIFF\x10\x00\x00\x00WEBP"), []byte("VP8 ")...)
	cases := map[string]struct {
		data []byte
		ext  string
	}{
		"png":   {iconTestPNG(), ".png"},
		"jpeg":  {append([]byte("\xFF\xD8\xFF\xE0"), []byte("jfif")...), ".jpg"},
		"gif87": {append([]byte("GIF87a"), []byte("xx")...), ".gif"},
		"gif89": {append([]byte("GIF89a"), []byte("xx")...), ".gif"},
		"webp":  {webp, ".webp"},
		"ico":   {append([]byte{0, 0, 1, 0}, []byte("ico")...), ".ico"},
		"svg":   {iconTestSVG(), ".svg"},
	}
	for name, c := range cases {
		ref, err := StoreIcon(c.data)
		if err != nil {
			t.Fatalf("%s: StoreIcon: %v", name, err)
		}
		if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, c.ext) {
			t.Errorf("%s: ref = %q, want file:*%s", name, ref, c.ext)
		}
	}
}

func TestIconTestSVGInTxtAccepted(t *testing.T) {
	iconTestEnv(t)
	src := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(src, iconTestSVG(), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatalf("StoreIconFile(.txt svg): %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Errorf("ref = %q, want .svg suffix", ref)
	}
}

func TestIconTestTextRejected(t *testing.T) {
	iconTestEnv(t)
	if _, err := StoreIcon([]byte("just some plain text, not an image")); err == nil {
		t.Error("plain text accepted, want error")
	}
	src := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(src, []byte("hello"), 0o644)
	if _, err := StoreIconFile(src); err == nil {
		t.Error("StoreIconFile(text file) accepted, want error")
	}
}

func TestIconTestEmptyRejected(t *testing.T) {
	iconTestEnv(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("nil data accepted, want error")
	}
	if _, err := StoreIcon([]byte{}); err == nil {
		t.Error("empty data accepted, want error")
	}
	src := filepath.Join(t.TempDir(), "empty.png")
	os.WriteFile(src, nil, 0o644)
	if _, err := StoreIconFile(src); err == nil {
		t.Error("empty file accepted, want error")
	}
}

func TestIconTestSizeBoundary(t *testing.T) {
	iconTestEnv(t)
	if MaxIcon != 1<<20 {
		t.Fatalf("MaxIcon = %d, want 1<<20", MaxIcon)
	}
	exact := iconTestPad(iconTestPNG(), MaxIcon)
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("MaxIcon bytes rejected: %v", err)
	}
	over := iconTestPad(iconTestPNG(), MaxIcon+1)
	if _, err := StoreIcon(over); err == nil {
		t.Error("MaxIcon+1 bytes accepted, want error")
	}
	src := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(src, over, 0o644)
	if _, err := StoreIconFile(src); err == nil {
		t.Error("oversized file accepted, want error")
	}
}

func TestIconTestNameIsHashPrefix(t *testing.T) {
	iconTestEnv(t)
	data := iconTestPNG()
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := "file:" + hex.EncodeToString(sum[:8]) + ".png"
	if ref != want {
		t.Errorf("ref = %q, want %q", ref, want)
	}
}

func TestIconTestRoundTripAndDedupe(t *testing.T) {
	home := iconTestEnv(t)
	data := iconTestPNG()
	ref1, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := StoreIcon(append([]byte(nil), data...))
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ref2 {
		t.Errorf("same content gave %q and %q", ref1, ref2)
	}
	entries, err := os.ReadDir(filepath.Join(home, "magpie", "icons"))
	if err != nil {
		t.Fatalf("icons dir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("icons dir has %d files, want 1", len(entries))
	}
	got, err := os.ReadFile(IconFile(strings.TrimPrefix(ref1, "file:")))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("stored content differs from original")
	}
}

func TestIconTestIconFileRejectsBadNames(t *testing.T) {
	iconTestEnv(t)
	bad := []string{
		"", "../providers.json", "x.png", "0123456789abcdef.exe",
		"0123456789ABCDEF.png", "0123456789abcdef.jpeg", "0123456789abcde.png",
		"0123456789abcdef0.png", "sub/0123456789abcdef.png", "..0123456789abcdef.png",
		"0123456789abcdef.png/", "file:0123456789abcdef.png",
	}
	for _, n := range bad {
		if got := IconFile(n); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", n, got)
		}
	}
	if got := IconFile("0123456789abcdef.png"); got == "" {
		t.Error("well-formed name rejected")
	}
}

func TestIconTestPruneKeepsRecentAndReferenced(t *testing.T) {
	iconTestEnv(t)
	oldRef, _ := StoreIcon(iconTestPNG())
	oldUnref, _ := StoreIcon(append([]byte("\x89PNG\r\n\x1a\n"), []byte("other")...))
	recent, _ := StoreIcon(append([]byte("\x89PNG\r\n\x1a\n"), []byte("recent")...))
	past := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{oldRef, oldUnref} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), past, past); err != nil {
			t.Fatal(err)
		}
	}
	refs := map[string]bool{oldRef: true, "generic": true, "": true}
	if err := pruneIcons(refs, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(oldRef, "file:"))); err != nil {
		t.Errorf("referenced old icon removed: %v", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(oldUnref, "file:"))); !os.IsNotExist(err) {
		t.Errorf("unreferenced old icon kept (err=%v)", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(recent, "file:"))); err != nil {
		t.Errorf("recent unreferenced icon removed: %v", err)
	}
}
