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

var storedNameRe = regexp.MustCompile(`^file:[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// pngOf returns a PNG-looking picture whose content is made unique by n.
func pngOf(n int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(strings.Repeat("x", 16)+string(rune('a'+n%26))+strings.Repeat("y", n))...)
}

func TestStoreIconKeepsOneCopy(t *testing.T) {
	freshHome(t)
	data := pngOf(1)
	a, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	if !storedNameRe.MatchString(a) {
		t.Fatalf("bad icon reference %q", a)
	}
	b, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same picture stored under two names: %q and %q", a, b)
	}
	ents, err := os.ReadDir(IconsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Errorf("expected one stored file, found %d", len(ents))
	}
	got, err := os.ReadFile(IconFile(strings.TrimPrefix(a, "file:")))
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("stored content does not match: %v", err)
	}
}

func TestStoreIconFormats(t *testing.T) {
	freshHome(t)
	cases := map[string][]byte{
		"jpg":  append([]byte{0xff, 0xd8, 0xff, 0xe0}, []byte("jpegdata")...),
		"gif":  []byte("GIF89a-data"),
		"webp": append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("VP8 data")...),
		"ico":  []byte{0, 0, 1, 0, 1, 0, 16, 16},
		"svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
	}
	for ext, data := range cases {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Errorf("%s rejected: %v", ext, err)
			continue
		}
		if !strings.HasSuffix(ref, "."+ext) {
			t.Errorf("%s stored as %q", ext, ref)
		}
	}
	if _, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)); err != nil {
		t.Errorf("xml-declared svg rejected: %v", err)
	}
}

func TestStoreIconRejects(t *testing.T) {
	freshHome(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty icon accepted")
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil {
		t.Errorf("icon of %d bytes accepted", len(big))
	}
	for _, text := range []string{"hello world", "<html><body></body></html>", "{\"json\": true}"} {
		if _, err := StoreIcon([]byte(text)); err == nil {
			t.Errorf("plain text %q accepted as an icon", text)
		}
	}
	if _, err := os.Stat(IconsDir()); err == nil {
		ents, _ := os.ReadDir(IconsDir())
		if len(ents) != 0 {
			t.Errorf("rejected icons were stored: %d files", len(ents))
		}
	}
}

func TestIconFileOnlyAcceptsStoredNames(t *testing.T) {
	freshHome(t)
	ok := "0123456789abcdef.png"
	if got := IconFile(ok); got != filepath.Join(IconsDir(), ok) {
		t.Errorf("IconFile(%q) = %q", ok, got)
	}
	for _, bad := range []string{
		"", "../0123456789abcdef.png", "sub/0123456789abcdef.png", `sub\0123456789abcdef.png`,
		"0123456789abcdef.exe", "0123456789ABCDEF.png", "0123456789abcdef.jpeg", "short.png",
		"0123456789abcdef.png/..", "..png", "/etc/passwd", "file:0123456789abcdef.png",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestSaveRejectsIconReferencesOutsideIcons(t *testing.T) {
	freshHome(t)
	for _, icon := range []string{"file:../providers.json", "file:../../etc/passwd", "../../secret.png", `a\b`} {
		err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Icon: icon})
		if err == nil {
			t.Errorf("icon %q accepted", icon)
		}
	}
}

func TestSaveRemovesUnusedOldIcons(t *testing.T) {
	freshHome(t)
	kept, err := StoreIcon(pngOf(2))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := StoreIcon(pngOf(3))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon(pngOf(4))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{kept, stale} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Icon: kept}); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(kept) {
		t.Error("referenced icon was removed")
	}
	if exists(stale) {
		t.Error("old unreferenced icon was kept")
	}
	if !exists(fresh) {
		t.Error("recent unreferenced icon was removed; it may not be saved yet")
	}
}
