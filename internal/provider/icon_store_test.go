package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// iconStoreEnv points the config directory at a fresh temp dir and returns
// the icons directory inside it.
func iconStoreEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return filepath.Join(filepath.Dir(Path()), "icons")
}

func iconStorePNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1, 2, 3}, 20)...)
}

func TestIconStoreDetectsFormats(t *testing.T) {
	iconStoreEnv(t)
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", iconStorePNG(), ".png"},
		{"jpeg", append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("jfif-data")...), ".jpg"},
		{"gif87", []byte("GIF87a-body"), ".gif"},
		{"gif89", []byte("GIF89a-body"), ".gif"},
		{"webp", append([]byte("RIFF\x10\x00\x00\x00WEBP"), []byte("VP8 data")...), ".webp"},
		{"ico", []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, ".ico"},
		{"svg", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`), ".svg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := StoreIcon(c.data)
			if err != nil {
				t.Fatalf("StoreIcon: %v", err)
			}
			sum := sha256.Sum256(c.data)
			want := "file:" + hex.EncodeToString(sum[:])[:16] + c.ext
			if ref != want {
				t.Fatalf("ref = %q, want %q", ref, want)
			}
		})
	}
}

func TestIconStoreSVGWithTxtExtension(t *testing.T) {
	iconStoreEnv(t)
	path := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(path, []byte(`<svg><circle r="1"/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(path)
	if err != nil {
		t.Fatalf("StoreIconFile: %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("ref = %q, want .svg suffix", ref)
	}
}

func TestIconStoreRejectsBadContent(t *testing.T) {
	iconStoreEnv(t)
	if _, err := StoreIcon([]byte("just some plain text")); err == nil {
		t.Fatal("plain text accepted")
	}
	if _, err := StoreIcon(nil); err == nil {
		t.Fatal("empty data accepted")
	}
	if _, err := StoreIcon([]byte{}); err == nil {
		t.Fatal("zero-length data accepted")
	}
	big := make([]byte, MaxIcon+1)
	copy(big, iconStorePNG())
	if _, err := StoreIcon(big); err == nil || !strings.Contains(err.Error(), "smaller") {
		t.Fatalf("oversized data: err = %v, want a 'smaller image' hint", err)
	}
}

func TestIconStoreAcceptsExactlyMaxIcon(t *testing.T) {
	iconStoreEnv(t)
	data := make([]byte, MaxIcon)
	copy(data, iconStorePNG())
	if _, err := StoreIcon(data); err != nil {
		t.Fatalf("exactly MaxIcon bytes rejected: %v", err)
	}
}

func TestIconStoreFileRejectsOversizeAndMissing(t *testing.T) {
	dir := iconStoreEnv(t)
	big := make([]byte, MaxIcon+1)
	copy(big, iconStorePNG())
	bigPath := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(bigPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreIconFile(bigPath); err == nil {
		t.Fatal("oversized file accepted")
	}
	if _, err := StoreIconFile(filepath.Join(t.TempDir(), "nope.png")); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := os.Stat(dir); err == nil {
		entries, _ := os.ReadDir(dir)
		if len(entries) != 0 {
			t.Fatalf("rejected icons left files behind: %d", len(entries))
		}
	}
}

func TestIconStoreDedupesIdenticalContent(t *testing.T) {
	dir := iconStoreEnv(t)
	data := iconStorePNG()
	ref1, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := StoreIcon(append([]byte(nil), data...))
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ref2 {
		t.Fatalf("refs differ: %q vs %q", ref1, ref2)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("icons dir has %d entries, want 1", len(entries))
	}
	if entries[0].Name() != strings.TrimPrefix(ref1, "file:") {
		t.Fatalf("stored name %q does not match ref %q", entries[0].Name(), ref1)
	}
}

func TestIconStoreFileRoundTrip(t *testing.T) {
	iconStoreEnv(t)
	data := iconStorePNG()
	src := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatalf("StoreIconFile: %v", err)
	}
	// The source may disappear; the stored copy must still resolve.
	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	if path == "" {
		t.Fatalf("IconFile(%q) returned empty", ref)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("stored content differs from source")
	}
}

func TestIconStoreIconFileRejectsInvalidNames(t *testing.T) {
	iconStoreEnv(t)
	for _, name := range []string{
		"../providers.json",
		"x.png",
		"0123456789abcdef.exe",
		"",
		"0123456789ABCDEF.png",
		"0123456789abcdef.PNG",
		"0123456789abcde.png",
		"0123456789abcdef0.png",
		"../0123456789abcdef.png",
		"sub/0123456789abcdef.png",
		"0123456789abcdef.png/",
		"file:0123456789abcdef.png",
	} {
		if got := IconFile(name); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", name, got)
		}
	}
}

func TestIconStoreIconFileAcceptsValidName(t *testing.T) {
	dir := iconStoreEnv(t)
	got := IconFile("0123456789abcdef.svg")
	if got != filepath.Join(dir, "0123456789abcdef.svg") {
		t.Fatalf("IconFile = %q", got)
	}
}
