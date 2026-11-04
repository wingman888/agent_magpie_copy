package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// iconTestIsolate points the providers file (and so the icons folder) at a
// fresh temp directory for the duration of one test.
func iconTestIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func iconTestPNG() []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte("png-body")...)
}

func iconTestExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestStoreIconPNG(t *testing.T) {
	iconTestIsolate(t)
	data := iconTestPNG()
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatalf("StoreIcon(png): %v", err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("ref = %q, want file:<hex>.png", ref)
	}
	if len(strings.TrimSuffix(strings.TrimPrefix(ref, "file:"), ".png")) != 16 {
		t.Fatalf("ref = %q, want 16 hex digits before the extension", ref)
	}
	got, err := os.ReadFile(IconFile(ref))
	if err != nil {
		t.Fatalf("reading stored icon: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("stored content differs from the original")
	}
}

func TestStoreIconDetectsTypes(t *testing.T) {
	iconTestIsolate(t)
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, ".jpg"},
		{"gif87", []byte("GIF87a...."), ".gif"},
		{"gif89", []byte("GIF89a...."), ".gif"},
		{"webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), ".webp"},
		{"ico", []byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, ".ico"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), ".svg"},
	}
	for _, c := range cases {
		ref, err := StoreIcon(c.data)
		if err != nil {
			t.Fatalf("%s: StoreIcon: %v", c.name, err)
		}
		if !strings.HasSuffix(ref, c.ext) {
			t.Fatalf("%s: ref = %q, want suffix %s", c.name, ref, c.ext)
		}
	}
}

func TestStoreIconFileSVGWithTxtExtension(t *testing.T) {
	iconTestIsolate(t)
	src := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(src, []byte(`<svg viewBox="0 0 1 1"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatalf("StoreIconFile(svg as .txt): %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("ref = %q, want .svg suffix", ref)
	}
}

func TestStoreIconRejectsText(t *testing.T) {
	iconTestIsolate(t)
	if _, err := StoreIcon([]byte("just some plain text")); err == nil {
		t.Fatal("plain text accepted as an icon")
	}
}

func TestStoreIconRejectsEmpty(t *testing.T) {
	iconTestIsolate(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Fatal("empty data accepted")
	}
	src := filepath.Join(t.TempDir(), "empty.png")
	if err := os.WriteFile(src, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreIconFile(src); err == nil {
		t.Fatal("empty file accepted")
	}
}

func TestStoreIconSizeLimit(t *testing.T) {
	iconTestIsolate(t)
	exact := append(iconTestPNG(), make([]byte, MaxIcon-len(iconTestPNG()))...)
	if len(exact) != MaxIcon {
		t.Fatalf("test setup: len = %d", len(exact))
	}
	if _, err := StoreIcon(exact); err != nil {
		t.Fatalf("exactly MaxIcon bytes refused: %v", err)
	}
	over := append(exact, 0)
	_, err := StoreIcon(over)
	if err == nil {
		t.Fatal("MaxIcon+1 bytes accepted")
	}
	if !strings.Contains(err.Error(), "use a smaller image") {
		t.Fatalf("error %q should suggest a smaller image", err)
	}

	src := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(src, over, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreIconFile(src); err == nil {
		t.Fatal("oversized file accepted by StoreIconFile")
	}
}

func TestStoreIconFileMissing(t *testing.T) {
	iconTestIsolate(t)
	if _, err := StoreIconFile(filepath.Join(t.TempDir(), "nope.png")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestStoreIconDeduplicates(t *testing.T) {
	iconTestIsolate(t)
	a, err := StoreIcon(iconTestPNG())
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(iconTestPNG())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same content gave %q and %q", a, b)
	}
	entries, err := os.ReadDir(iconDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("icons folder has %d files, want 1", len(entries))
	}
}

func TestIconFileRejectsBadNames(t *testing.T) {
	iconTestIsolate(t)
	bad := []string{
		"",
		"../providers.json",
		"x.png",
		"0123456789abcdef.exe",
		"0123456789ABCDEF.png",
		"0123456789abcdef0.png",
		"0123456789abcde.png",
		"sub/0123456789abcdef.png",
		"file:",
		"file:../providers.json",
		"0123456789abcdef.png\n",
	}
	for _, name := range bad {
		if got := IconFile(name); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", name, got)
		}
	}
}

func TestIconFileAcceptsStoredName(t *testing.T) {
	iconTestIsolate(t)
	data := iconTestPNG()
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	path := IconFile(ref)
	if path == "" {
		t.Fatalf("IconFile(%q) empty", ref)
	}
	if got := IconFile(strings.TrimPrefix(ref, "file:")); got != path {
		t.Fatalf("bare name gave %q, want %q", got, path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("IconFile path does not hold the stored image: %v", err)
	}
}

func TestPruneIconsKeepsReferencedAndFresh(t *testing.T) {
	iconTestIsolate(t)
	old := time.Now().Add(-2 * time.Hour)

	refd, err := StoreIcon(iconTestPNG())
	if err != nil {
		t.Fatal(err)
	}
	orphan, err := StoreIcon([]byte("<svg></svg>"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon([]byte("GIF89a-fresh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{refd, orphan} {
		if err := os.Chtimes(IconFile(ref), old, old); err != nil {
			t.Fatal(err)
		}
	}

	// Saving a provider that uses refd prunes: refd is referenced, orphan is
	// old and unused, fresh is within the hour.
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k", Icon: refd}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !iconTestExists(IconFile(refd)) {
		t.Error("referenced icon was pruned")
	}
	if iconTestExists(IconFile(orphan)) {
		t.Error("old unreferenced icon was kept")
	}
	if !iconTestExists(IconFile(fresh)) {
		t.Error("fresh unreferenced icon was pruned")
	}

	// Deleting the provider leaves refd unreferenced and old: it goes.
	if err := Delete("relay"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if iconTestExists(IconFile(refd)) {
		t.Error("icon no longer referenced was kept after Delete")
	}
	if !iconTestExists(IconFile(fresh)) {
		t.Error("fresh icon pruned after Delete")
	}
}

func TestPruneIconsIgnoresBuiltinIconNames(t *testing.T) {
	iconTestIsolate(t)
	ref, err := StoreIcon(iconTestPNG())
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(IconFile(ref), old, old); err != nil {
		t.Fatal(err)
	}
	// A built-in icon name and an empty icon are not references to the image.
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if iconTestExists(IconFile(ref)) {
		t.Error("old unreferenced icon kept because a provider had a built-in icon name")
	}
}
