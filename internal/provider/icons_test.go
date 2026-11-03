package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateIcons points the config directory at a fresh temp dir.
func isolateIcons(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	return home
}

func pngData(extra string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(extra)...)
}

func iconEntries(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(iconDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestStoreIconTypesAndNames(t *testing.T) {
	isolateIcons(t)
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), []byte("VP8 ")...)
	cases := map[string]struct {
		data []byte
		ext  string
	}{
		"png":   {pngData("a"), ".png"},
		"jpeg":  {[]byte{0xFF, 0xD8, 0xFF, 0xE0, 'j'}, ".jpg"},
		"gif87": {[]byte("GIF87a....."), ".gif"},
		"gif89": {[]byte("GIF89a....."), ".gif"},
		"webp":  {webp, ".webp"},
		"ico":   {[]byte{0, 0, 1, 0, 'i'}, ".ico"},
		"svg":   {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), ".svg"},
	}
	for name, c := range cases {
		ref, err := StoreIcon(c.data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, c.ext) {
			t.Fatalf("%s: ref = %q, want file:…%s", name, ref, c.ext)
		}
		fname := strings.TrimPrefix(ref, "file:")
		if len(fname) != 16+len(c.ext) {
			t.Errorf("%s: name %q has wrong length", name, fname)
		}
		got, err := os.ReadFile(IconFile(fname))
		if err != nil || !bytes.Equal(got, c.data) {
			t.Errorf("%s: stored content mismatch (err=%v)", name, err)
		}
	}
}

func TestStoreIconDedup(t *testing.T) {
	isolateIcons(t)
	a, err := StoreIcon(pngData("same"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(pngData("same"))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("refs differ: %q vs %q", a, b)
	}
	if n := len(iconEntries(t)); n != 1 {
		t.Errorf("%d files stored, want 1", n)
	}
}

func TestStoreIconRejects(t *testing.T) {
	isolateIcons(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty data accepted")
	}
	if _, err := StoreIcon([]byte("just some text, not an image")); err == nil {
		t.Error("plain text accepted")
	}
	big := append(pngData(""), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil || !strings.Contains(err.Error(), "smaller") {
		t.Errorf("oversized icon: err = %v, want a 'smaller image' hint", err)
	}
	exact := append(pngData(""), make([]byte, MaxIcon-8)...)
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("icon of exactly MaxIcon bytes rejected: %v", err)
	}
}

func TestStoreIconFile(t *testing.T) {
	dir := t.TempDir()
	svgTxt := filepath.Join(dir, "logo.txt")
	if err := os.WriteFile(svgTxt, []byte(`<svg></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(svgTxt)
	if err != nil {
		t.Fatalf("svg with .txt extension: %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Errorf("ref = %q, want .svg", ref)
	}
	if _, err := StoreIconFile(filepath.Join(dir, "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
	empty := filepath.Join(dir, "empty.png")
	os.WriteFile(empty, nil, 0o644)
	if _, err := StoreIconFile(empty); err == nil {
		t.Error("empty file accepted")
	}
	text := filepath.Join(dir, "x.png")
	os.WriteFile(text, []byte("hello"), 0o644)
	if _, err := StoreIconFile(text); err == nil {
		t.Error("text file named .png accepted")
	}
}

func TestIconFileNames(t *testing.T) {
	isolateIcons(t)
	ref, err := StoreIcon(pngData("name"))
	if err != nil {
		t.Fatal(err)
	}
	if IconFile(strings.TrimPrefix(ref, "file:")) == "" {
		t.Error("generated name rejected")
	}
	for _, bad := range []string{
		"../providers.json",
		"x.png",
		"0123456789abcdef.exe",
		"",
		"0123456789ABCDEF.png",
		"0123456789abcdef.png/..",
		"012345678/abcdef.png",
		"0123456789abcdef0.png",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestIconGC(t *testing.T) {
	isolateIcons(t)
	old := time.Now().Add(-2 * time.Hour)

	keep, err := StoreIcon(pngData("keep"))
	if err != nil {
		t.Fatal(err)
	}
	drop, err := StoreIcon(pngData("drop"))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{keep, drop} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// A saved provider refers to "keep"; "drop" is unreferenced and old.
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k", Icon: keep}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(IconFile(strings.TrimPrefix(keep, "file:"))) {
		t.Error("referenced icon was removed")
	}
	if fileExists(IconFile(strings.TrimPrefix(drop, "file:"))) {
		t.Error("unreferenced old icon was kept")
	}

	// Changing the provider's icon to a built-in name orphans "keep".
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k", Icon: "generic"}); err != nil {
		t.Fatal(err)
	}
	if fileExists(IconFile(strings.TrimPrefix(keep, "file:"))) {
		t.Error("icon no longer referenced (builtin name now) was kept")
	}

	// A freshly stored, unreferenced icon survives GC.
	fresh, err := StoreIcon(pngData("fresh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(IconFile(strings.TrimPrefix(fresh, "file:"))) {
		t.Error("icon stored within the last hour was removed")
	}
}

func TestIconGCOnDelete(t *testing.T) {
	isolateIcons(t)
	ref, err := StoreIcon(pngData("del"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "c", Name: "C", Chat: "https://c.example/v1", Key: "k", Icon: ref}); err != nil {
		t.Fatal(err)
	}
	path := IconFile(strings.TrimPrefix(ref, "file:"))
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := Delete("c"); err != nil {
		t.Fatal(err)
	}
	// Delete writes providers.json, which triggers GC.
	if fileExists(path) {
		t.Error("icon of a deleted provider was kept")
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
