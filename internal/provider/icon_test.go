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

var iconRef = regexp.MustCompile(`^file:[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

func pngBytes(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
	return b
}

func TestStoreIconStoresOncePerContent(t *testing.T) {
	isolateConfig(t)
	img := pngBytes(64)
	ref, err := StoreIcon(img)
	if err != nil {
		t.Fatal(err)
	}
	if !iconRef.MatchString(ref) {
		t.Fatalf("ref = %q, want file:<16 hex>.png", ref)
	}
	again, err := StoreIcon(img)
	if err != nil || again != ref {
		t.Fatalf("second store = %q, %v; want %q", again, err, ref)
	}
	entries, err := os.ReadDir(IconDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("icons folder has %d files, want 1", len(entries))
	}
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	if path == "" {
		t.Fatalf("IconFile rejects a name StoreIcon made: %q", ref)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, img) {
		t.Fatalf("stored file does not match the image: %v", err)
	}
}

func TestStoreIconFormats(t *testing.T) {
	isolateConfig(t)
	cases := map[string][]byte{
		".jpg":  append([]byte{0xff, 0xd8, 0xff, 0xe0}, make([]byte, 20)...),
		".gif":  []byte("GIF89a\x01\x00\x01\x00"),
		".webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 "),
		".ico":  []byte{0, 0, 1, 0, 1, 0},
		".svg":  []byte(`<?xml version="1.0"?>` + "\n<!-- logo -->\n" + `<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
	}
	for ext, data := range cases {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Errorf("%s: %v", ext, err)
			continue
		}
		if !strings.HasSuffix(ref, ext) {
			t.Errorf("%s stored as %q", ext, ref)
		}
	}
}

func TestStoreIconRefusesBadInput(t *testing.T) {
	isolateConfig(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty content accepted")
	}
	if _, err := StoreIcon(pngBytes(MaxIcon)); err == nil {
		t.Error("content over MaxIcon accepted")
	}
	if _, err := StoreIcon(pngBytes(MaxIcon - 8)); err != nil {
		t.Errorf("content of exactly MaxIcon refused: %v", err)
	}
	for _, text := range []string{
		"hello, this is plain text",
		"<html><body><svg></svg></body></html>",
		"<svg-like>not svg</svg-like>",
		"<!doctype html><html></html>",
		"\x00\x01binary",
	} {
		if _, err := StoreIcon([]byte(text)); err == nil {
			t.Errorf("non-image %q accepted", text)
		}
	}
	// Only the one accepted image (exactly MaxIcon) may be in the folder.
	entries, _ := os.ReadDir(IconDir())
	if len(entries) != 1 {
		t.Errorf("icons folder has %d files, want only the accepted image", len(entries))
	}
}

func TestIconFileOnlyAcceptsStoredNames(t *testing.T) {
	isolateConfig(t)
	good := "0123456789abcdef.png"
	if got := IconFile(good); got != filepath.Join(IconDir(), good) {
		t.Errorf("IconFile(%q) = %q", good, got)
	}
	for _, bad := range []string{
		"",
		"0123456789abcdef.exe",
		"0123456789abcdef",
		"0123456789ABCDEF.png",
		"0123456789abcde.png",
		"0123456789abcdef0.png",
		"../0123456789abcdef.png",
		"..\\0123456789abcdef.png",
		"sub/0123456789abcdef.png",
		"../../providers.json",
		"0123456789abcdef.png/..",
		"0123456789abcdef.png\n",
		"/etc/passwd",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestSaveRefusesIconsOutsideTheFolder(t *testing.T) {
	isolateConfig(t)
	err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk",
		Icon: "file:../../providers.json"})
	if err == nil {
		t.Fatal("a reference outside the icons folder was saved")
	}
}

func TestSavePrunesUnusedOldIcons(t *testing.T) {
	isolateConfig(t)
	dir := IconDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	touch := func(name string, when time.Time) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, when, when); err != nil {
			t.Fatal(err)
		}
	}
	kept := "1111111111111111.png" // referenced by a provider, though old
	orphanOld := "2222222222222222.png"
	orphanNew := "3333333333333333.png"
	notIcon := "notes.txt"
	touch(kept, old)
	touch(orphanOld, old)
	touch(orphanNew, time.Now())
	touch(notIcon, old)

	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk",
		Icon: "file:" + kept}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "Other", Chat: "https://other.example.com/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}
	if !exists(kept) {
		t.Error("a referenced icon was removed")
	}
	if exists(orphanOld) {
		t.Error("an unreferenced icon older than an hour was kept")
	}
	if !exists(orphanNew) {
		t.Error("an unreferenced icon less than an hour old was removed")
	}
	if !exists(notIcon) {
		t.Error("a file that is not an icon name was removed")
	}
}

func TestStoreIconTouchesExistingFile(t *testing.T) {
	isolateConfig(t)
	img := pngBytes(32)
	ref, err := StoreIcon(img)
	if err != nil {
		t.Fatal(err)
	}
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreIcon(img); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(info.ModTime()) > time.Minute {
		t.Error("picking an existing image did not refresh its grace period")
	}
}
