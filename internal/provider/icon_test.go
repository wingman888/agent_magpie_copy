package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pngBytes(payload string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(payload)...)
}

func iconsDirOf(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "magpie", "icons")
}

func TestStoreIconNameAndSingleCopy(t *testing.T) {
	isolateConfig(t)
	data := pngBytes("one")
	v1, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v1, "file:") || !strings.HasSuffix(v1, ".png") {
		t.Fatalf("icon value %q", v1)
	}
	name := strings.TrimPrefix(v1, "file:")
	if len(name) != 16+len(".png") {
		t.Fatalf("name %q: want 16 hex digits and .png", name)
	}
	path := IconFile(name)
	if path == "" {
		t.Fatalf("IconFile(%q) rejected a name StoreIcon produced", name)
	}
	if filepath.Dir(path) != iconsDirOf(t) {
		t.Errorf("stored in %s, want the icons folder", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stored bytes differ: %v", err)
	}

	v2, err := StoreIcon(data)
	if err != nil || v2 != v1 {
		t.Fatalf("same image gave %q, %v; want %q", v2, err, v1)
	}
	ents, _ := os.ReadDir(iconsDirOf(t))
	if len(ents) != 1 {
		t.Errorf("want one file for one image, have %d", len(ents))
	}
}

func TestStoreIconSniffsTypes(t *testing.T) {
	isolateConfig(t)
	cases := map[string][]byte{
		".jpg":  append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("jfif")...),
		".gif":  []byte("GIF89a....."),
		".webp": append([]byte("RIFF\x10\x00\x00\x00WEBP"), []byte("VP8 ")...),
		".ico":  {0, 0, 1, 0, 1, 0, 16, 16},
		".svg":  []byte(`<?xml version="1.0"?><!-- c --><svg xmlns="http://www.w3.org/2000/svg"/>`),
	}
	for ext, data := range cases {
		v, err := StoreIcon(data)
		if err != nil {
			t.Errorf("%s: %v", ext, err)
			continue
		}
		if !strings.HasSuffix(v, ext) {
			t.Errorf("%s stored as %q", ext, v)
		}
	}
}

func TestStoreIconRejects(t *testing.T) {
	isolateConfig(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty content accepted")
	}
	if _, err := StoreIcon([]byte("just some text, not an image")); err == nil {
		t.Error("plain text accepted")
	}
	if _, err := StoreIcon([]byte("<html><body>hi</body></html>")); err == nil {
		t.Error("html accepted as svg")
	}
	big := pngBytes(strings.Repeat("x", MaxIcon))
	if len(big) <= MaxIcon {
		t.Fatal("test setup: image should exceed MaxIcon")
	}
	if _, err := StoreIcon(big); err == nil {
		t.Error("image over 1 MB accepted")
	}
	exact := append(pngBytes(""), make([]byte, MaxIcon-len(pngBytes("")))...)
	if len(exact) != MaxIcon {
		t.Fatal("test setup: want exactly MaxIcon bytes")
	}
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("image of exactly 1 MB rejected: %v", err)
	}
}

func TestIconFileOnlyAcceptsStoredShape(t *testing.T) {
	isolateConfig(t)
	good := "0123456789abcdef.png"
	if got := IconFile(good); filepath.Base(got) != good || filepath.Dir(got) != iconsDirOf(t) {
		t.Errorf("IconFile(%q) = %q", good, got)
	}
	for _, bad := range []string{
		"", "../0123456789abcdef.png", "..", "a/0123456789abcdef.png",
		`a\0123456789abcdef.png`, "0123456789abcdef.jpeg", "0123456789ABCDEF.png",
		"0123456789abcde.png", "0123456789abcdef.exe", "file:0123456789abcdef.png",
		"../../providers.json", "/etc/passwd",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestPruneIconsKeepsReferencedAndRecent(t *testing.T) {
	isolateConfig(t)
	oldRef, _ := StoreIcon(pngBytes("referenced"))
	oldLoose, _ := StoreIcon(pngBytes("loose"))
	recent, _ := StoreIcon(pngBytes("recent"))
	foreign := filepath.Join(iconsDirOf(t), "notes.txt")
	os.WriteFile(foreign, []byte("keep me"), 0o644)

	long := time.Now().Add(-2 * time.Hour)
	for _, v := range []string{oldRef, oldLoose} {
		os.Chtimes(IconFile(strings.TrimPrefix(v, "file:")), long, long)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1", Icon: oldRef}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(IconFile(strings.TrimPrefix(oldLoose, "file:"))); !os.IsNotExist(err) {
		t.Error("old unreferenced icon was not removed")
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(oldRef, "file:"))); err != nil {
		t.Errorf("referenced icon removed: %v", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(recent, "file:"))); err != nil {
		t.Errorf("recent unreferenced icon removed: %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Errorf("non-icon file removed: %v", err)
	}
}

func TestSaveRejectsBadIconValues(t *testing.T) {
	isolateConfig(t)
	base := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-1"}
	for _, icon := range []string{"file:../../providers.json", "file:0123456789abcdef.jpeg", "../../etc/passwd", "Bad Name"} {
		p := base
		p.Icon = icon
		if err := Save(p); err == nil {
			t.Errorf("Save accepted icon %q", icon)
		}
	}
	p := base
	p.Icon = "claude-color"
	if err := Save(p); err != nil {
		t.Errorf("built-in icon name rejected: %v", err)
	}
}

func TestIconFromArg(t *testing.T) {
	isolateConfig(t)
	if v, err := IconFromArg(""); err != nil || v != "" {
		t.Errorf(`"" = %q, %v; want cleared`, v, err)
	}
	if v, err := IconFromArg("openai"); err != nil || v != "openai" {
		t.Errorf("built-in name = %q, %v", v, err)
	}
	if _, err := IconFromArg("bad name!"); err == nil {
		t.Error("garbage name accepted")
	}
	if _, err := IconFromArg(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Error("missing file accepted")
	}

	dir := t.TempDir()
	png := filepath.Join(dir, "logo.png")
	os.WriteFile(png, pngBytes("logo"), 0o644)
	v, err := IconFromArg(png)
	if err != nil || !strings.HasPrefix(v, "file:") {
		t.Fatalf("image file = %q, %v", v, err)
	}

	txt := filepath.Join(dir, "readme.txt")
	os.WriteFile(txt, []byte("hello"), 0o644)
	if _, err := IconFromArg(txt); err == nil {
		t.Error("text file accepted as icon")
	}
	big := filepath.Join(dir, "huge.png")
	os.WriteFile(big, append(pngBytes("x"), make([]byte, MaxIcon)...), 0o644)
	if _, err := IconFromArg(big); err == nil {
		t.Error("over-size file accepted")
	}
}
