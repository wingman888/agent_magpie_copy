package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateConfig points providers.json, its icons folder and the account
// lookups at temp directories, so a test never touches the real config.
func isolateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("fake picture body")...)

const testSVG = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><rect width="1" height="1"/></svg>`

func TestStoreIconFormats(t *testing.T) {
	isolateConfig(t)
	cases := map[string][]byte{
		".png":  testPNG,
		".jpg":  []byte("\xff\xd8\xff\xe0 jpeg body"),
		".gif":  []byte("GIF89a gif body"),
		".webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 body"),
		".ico":  {0, 0, 1, 0, 1, 0},
		".svg":  []byte(testSVG),
	}
	for ext, data := range cases {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ext) {
			t.Fatalf("%s: reference %q", ext, ref)
		}
		if _, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:"))); err != nil {
			t.Fatalf("%s: stored file missing: %v", ext, err)
		}
	}
}

func TestStoreIconRejects(t *testing.T) {
	isolateConfig(t)
	cases := map[string][]byte{
		"empty":     nil,
		"plain":     []byte("just some words"),
		"html":      []byte("<html><body>hi</body></html>"),
		"too large": append(append([]byte{}, testPNG...), make([]byte, MaxIcon)...),
	}
	for name, data := range cases {
		if ref, err := StoreIcon(data); err == nil {
			t.Errorf("%s: accepted as %q", name, ref)
		}
	}
}

func TestStoreIconOnlyOnce(t *testing.T) {
	isolateConfig(t)
	a, err := StoreIcon(testPNG)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(testPNG)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same picture, two references: %q %q", a, b)
	}
	entries, _ := os.ReadDir(iconDir())
	if len(entries) != 1 {
		t.Fatalf("%d files for one picture", len(entries))
	}
}

func TestIconFileOnlyStoredNames(t *testing.T) {
	isolateConfig(t)
	good := "0123456789abcdef.png"
	if got := IconFile(good); got != filepath.Join(iconDir(), good) {
		t.Fatalf("IconFile(%q) = %q", good, got)
	}
	for _, bad := range []string{
		"", "../providers.json", "sub/0123456789abcdef.png", `..\0123456789abcdef.png`,
		"0123456789abcdef.exe", "0123456789ABCDEF.png", "short.png", "file:0123456789abcdef.png",
		"0123456789abcdef.png/..", "..", "0123456789abcdef.png\x00",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestSaveKeepsUsedIconsAndPrunesOldOnes(t *testing.T) {
	isolateConfig(t)
	used, err := StoreIcon(testPNG)
	if err != nil {
		t.Fatal(err)
	}
	old, err := StoreIcon([]byte("GIF89a an old picture"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon([]byte("GIF89a a picture just chosen"))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{used, old} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-test", Icon: used}); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(used) {
		t.Error("the icon a provider uses was removed")
	}
	if exists(old) {
		t.Error("an unused icon older than an hour was kept")
	}
	if !exists(fresh) {
		t.Error("an unused icon from the last hour was removed")
	}
}

func TestSaveRejectsUnstoredIconReference(t *testing.T) {
	isolateConfig(t)
	err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-test", Icon: "file:../../providers.json"})
	if err == nil {
		t.Fatal("a reference outside the icons folder was saved")
	}
}
