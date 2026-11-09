package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// iconStoreEnv points config and home at a fresh directory for one test.
func iconStoreEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
}

// iconStoreImage returns a blob that starts with magic and is padded to size.
func iconStoreImage(magic []byte, size int) []byte {
	b := make([]byte, size)
	copy(b, magic)
	return b
}

// iconStoreAge sets a stored icon's modification time to now minus d.
func iconStoreAge(t *testing.T, path string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// iconStoreFiles lists the names in the icons directory.
func iconStoreFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(iconDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestIconStoreDetectsTypes(t *testing.T) {
	iconStoreEnv(t)
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), 64), ".png"},
		{"jpeg", iconStoreImage([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 64), ".jpg"},
		{"gif87", iconStoreImage([]byte("GIF87a"), 64), ".gif"},
		{"gif89", iconStoreImage([]byte("GIF89a"), 64), ".gif"},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 40)...), ".webp"},
		{"ico", iconStoreImage([]byte{0x00, 0x00, 0x01, 0x00}, 64), ".ico"},
		{"svg", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`), ".svg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := StoreIcon(c.data)
			if err != nil {
				t.Fatalf("StoreIcon: %v", err)
			}
			if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, c.ext) {
				t.Fatalf("ref = %q, want file:…%s", ref, c.ext)
			}
			name := strings.TrimPrefix(ref, "file:")
			if !regexp.MustCompile(`^[0-9a-f]{16}\` + c.ext + `$`).MatchString(name) {
				t.Fatalf("name %q is not 16 hex digits plus %s", name, c.ext)
			}
		})
	}
}

func TestIconStoreSVGWithTxtExtension(t *testing.T) {
	iconStoreEnv(t)
	src := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(src, []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 0o600); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatalf("StoreIconFile: %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("ref = %q, want .svg", ref)
	}
}

func TestIconStoreRejects(t *testing.T) {
	iconStoreEnv(t)
	if _, err := StoreIcon([]byte("just some text, not an image")); err == nil {
		t.Error("plain text was accepted")
	}
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty data was accepted")
	}
	if _, err := StoreIcon(iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), MaxIcon+1)); err == nil {
		t.Error("data over MaxIcon was accepted")
	} else if !strings.Contains(err.Error(), "smaller") {
		t.Errorf("oversize error %q does not suggest a smaller image", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.png")
	os.WriteFile(empty, nil, 0o600)
	if _, err := StoreIconFile(empty); err == nil {
		t.Error("empty file was accepted")
	}
	big := filepath.Join(t.TempDir(), "big.png")
	os.WriteFile(big, iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), MaxIcon+1), 0o600)
	if _, err := StoreIconFile(big); err == nil {
		t.Error("oversize file was accepted")
	}
	if _, err := StoreIconFile(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Error("missing file was accepted")
	}
}

func TestIconStoreAcceptsExactlyMax(t *testing.T) {
	iconStoreEnv(t)
	if _, err := StoreIcon(iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), MaxIcon)); err != nil {
		t.Fatalf("exactly MaxIcon rejected: %v", err)
	}
}

func TestIconStoreDeduplicates(t *testing.T) {
	iconStoreEnv(t)
	data := iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), 100)
	a, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same content gave %q and %q", a, b)
	}
	if n := len(iconStoreFiles(t)); n != 1 {
		t.Fatalf("icons dir has %d files, want 1", n)
	}
}

func TestIconStoreIconFileNames(t *testing.T) {
	iconStoreEnv(t)
	for _, bad := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", ""} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
	data := iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), 200)
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	if path == "" {
		t.Fatal("IconFile returned empty for a stored name")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("stored file content differs from the original")
	}
}

func TestIconStorePrunesUnreferenced(t *testing.T) {
	iconStoreEnv(t)
	refA, err := StoreIcon(iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), 300))
	if err != nil {
		t.Fatal(err)
	}
	refB, err := StoreIcon(iconStoreImage([]byte("GIF89a"), 300))
	if err != nil {
		t.Fatal(err)
	}
	refC, err := StoreIcon(iconStoreImage([]byte{0xFF, 0xD8, 0xFF, 0xE0}, 300))
	if err != nil {
		t.Fatal(err)
	}
	pathA := IconFile(strings.TrimPrefix(refA, "file:"))
	pathB := IconFile(strings.TrimPrefix(refB, "file:"))
	pathC := IconFile(strings.TrimPrefix(refC, "file:"))
	// A and B are two hours old; C was stored just now.
	iconStoreAge(t, pathA, 2*time.Hour)
	iconStoreAge(t, pathB, 2*time.Hour)

	// Saving a provider that uses A prunes the unreferenced old B, keeps A
	// (referenced) and keeps C (too new to prune).
	if err := Save(Provider{Name: "Icon Prune", Chat: "https://example.com/v1", Key: "k", Icon: refA}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pathB); !os.IsNotExist(err) {
		t.Errorf("old unreferenced icon was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(pathA); err != nil {
		t.Errorf("referenced icon was pruned: %v", err)
	}
	if _, err := os.Stat(pathC); err != nil {
		t.Errorf("recent icon was pruned: %v", err)
	}

	// Once the provider is deleted, A is unreferenced and old, so it goes.
	if err := Delete("icon-prune"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pathA); !os.IsNotExist(err) {
		t.Errorf("icon of deleted provider was not pruned (err=%v)", err)
	}
	if _, err := os.Stat(pathC); err != nil {
		t.Errorf("recent icon was pruned after delete: %v", err)
	}
}

func TestIconStoreRestoredIconSurvivesPrune(t *testing.T) {
	iconStoreEnv(t)
	data := iconStoreImage([]byte("\x89PNG\r\n\x1a\n"), 400)
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	iconStoreAge(t, path, 2*time.Hour)
	// Storing the same image again counts as a fresh store and refreshes it.
	if _, err := StoreIcon(data); err != nil {
		t.Fatal(err)
	}
	pruneIcons()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("re-stored icon was pruned: %v", err)
	}
}
