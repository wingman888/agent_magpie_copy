package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateConfig points the config directory at a fresh temp dir.
func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

func pad(head []byte, n int) []byte {
	b := make([]byte, n)
	copy(b, head)
	return b
}

func TestStoreIconFormats(t *testing.T) {
	isolateConfig(t)
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("body")...), ".png"},
		{"jpeg", append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("body")...), ".jpg"},
		{"gif87", []byte("GIF87a....body"), ".gif"},
		{"gif89", []byte("GIF89a....body"), ".gif"},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 body"), ".webp"},
		{"ico", []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, ".ico"},
		{"svg", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`), ".svg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := StoreIcon(c.data)
			if err != nil {
				t.Fatalf("StoreIcon: %v", err)
			}
			if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, c.ext) {
				t.Fatalf("ref = %q, want file:*%s", ref, c.ext)
			}
			path := IconFile(ref)
			if path == "" {
				t.Fatalf("IconFile(%q) empty", ref)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read stored icon: %v", err)
			}
			if !bytes.Equal(got, c.data) {
				t.Fatalf("stored content differs from input")
			}
		})
	}
}

func TestStoreIconSVGTxtFile(t *testing.T) {
	isolateConfig(t)
	src := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(src, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatalf("StoreIconFile svg .txt: %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("ref = %q, want .svg", ref)
	}
}

func TestStoreIconRejects(t *testing.T) {
	isolateConfig(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty data accepted")
	}
	if _, err := StoreIcon([]byte{}); err == nil {
		t.Error("zero-length data accepted")
	}
	if _, err := StoreIcon([]byte("just some plain text")); err == nil {
		t.Error("plain text accepted")
	}
	big := pad([]byte{0x89, 'P', 'N', 'G'}, MaxIcon+1)
	_, err := StoreIcon(big)
	if err == nil {
		t.Fatal("oversize data accepted")
	}
	if !strings.Contains(err.Error(), "under 1 MB") {
		t.Errorf("oversize error %q does not suggest a smaller image", err)
	}
}

func TestStoreIconMaxExactlyAccepted(t *testing.T) {
	isolateConfig(t)
	if MaxIcon != 1<<20 {
		t.Fatalf("MaxIcon = %d, want 1<<20", MaxIcon)
	}
	if _, err := StoreIcon(pad([]byte{0x89, 'P', 'N', 'G'}, MaxIcon)); err != nil {
		t.Fatalf("exactly MaxIcon rejected: %v", err)
	}
}

func TestStoreIconSameContentSameRef(t *testing.T) {
	isolateConfig(t)
	data := []byte("GIF89a-same-content")
	r1, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := StoreIcon(append([]byte(nil), data...))
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Fatalf("refs differ: %q vs %q", r1, r2)
	}
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("icons dir has %d files, want 1", len(entries))
	}
}

func TestStoreIconFileSizeCheck(t *testing.T) {
	isolateConfig(t)
	dir := t.TempDir()
	big := filepath.Join(dir, "big.png")
	if err := os.WriteFile(big, pad([]byte{0x89, 'P', 'N', 'G'}, MaxIcon+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreIconFile(big); err == nil {
		t.Error("oversize file accepted")
	}
	empty := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreIconFile(empty); err == nil {
		t.Error("empty file accepted")
	}
	if _, err := StoreIconFile(filepath.Join(dir, "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestIconFileValidation(t *testing.T) {
	isolateConfig(t)
	for _, name := range []string{
		"../providers.json",
		"x.png",
		"0123456789abcdef.exe",
		"",
		"0123456789ABCDEF.png", // uppercase hex is not what StoreIcon writes
		"0123456789abcde.png",  // 15 digits
		"0123456789abcdef0.png",
		"0123456789abcdef/../x.png",
		"file:",
	} {
		if got := IconFile(name); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", name, got)
		}
	}
}

func TestIconFileResolvesStoredIcon(t *testing.T) {
	isolateConfig(t)
	data := []byte("GIF89a-resolve")
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{ref, strings.TrimPrefix(ref, "file:")} {
		path := IconFile(in)
		if path == "" {
			t.Fatalf("IconFile(%q) empty", in)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("IconFile(%q) content mismatch: %v", in, err)
		}
	}
}

func TestPruneIcons(t *testing.T) {
	isolateConfig(t)
	oldRef, err := StoreIcon([]byte("GIF89a-old"))
	if err != nil {
		t.Fatal(err)
	}
	keptRef, err := StoreIcon([]byte("GIF89a-kept"))
	if err != nil {
		t.Fatal(err)
	}
	freshRef, err := StoreIcon([]byte("GIF89a-fresh"))
	if err != nil {
		t.Fatal(err)
	}
	twoHours := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{oldRef, keptRef} {
		if err := os.Chtimes(IconFile(ref), twoHours, twoHours); err != nil {
			t.Fatal(err)
		}
	}

	// oldRef is no longer referenced; keptRef is still referenced by a
	// provider; freshRef is unreferenced but recent.
	f := file{Providers: []Provider{
		{ID: "a", Icon: keptRef},
		{ID: "b", Icon: "deepseek-color"},
	}}
	pruneIcons(f)

	if _, err := os.Stat(IconFile(oldRef)); !os.IsNotExist(err) {
		t.Error("unreferenced old icon was not pruned")
	}
	if _, err := os.Stat(IconFile(keptRef)); err != nil {
		t.Errorf("referenced old icon was pruned: %v", err)
	}
	if _, err := os.Stat(IconFile(freshRef)); err != nil {
		t.Errorf("recent icon was pruned: %v", err)
	}
}

func TestStorePruneOnSave(t *testing.T) {
	isolateConfig(t)
	ref, err := StoreIcon([]byte("GIF89a-saved-then-replaced"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(IconFile(ref), old, old); err != nil {
		t.Fatal(err)
	}
	// A save that does not reference the icon should prune it.
	if err := store(file{Providers: []Provider{{ID: "x", Chat: "http://x", Icon: "generic"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(ref)); !os.IsNotExist(err) {
		t.Error("icon not pruned by store after it became unreferenced")
	}
}
