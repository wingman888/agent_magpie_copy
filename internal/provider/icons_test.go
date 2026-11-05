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

// accTestIconHome points every config and cache path at a temp dir.
func accTestIconHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func accTestPNG(extra int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, extra)...)
}

func accTestIconFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(IconDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAccStoreIconRejectsBadContent(t *testing.T) {
	accTestIconHome(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty content should be rejected")
	}
	if _, err := StoreIcon([]byte("just some text, not an image")); err == nil {
		t.Error("plain text should be rejected")
	}
	if _, err := StoreIcon(accTestPNG(MaxIcon)); err == nil {
		t.Error("an image over 1 MB should be rejected")
	}
	if _, err := StoreIcon(accTestPNG(MaxIcon - 8)); err != nil {
		t.Errorf("an image of exactly 1 MB should be kept: %v", err)
	}
}

func TestAccStoreIconStoresOncePerImage(t *testing.T) {
	accTestIconHome(t)
	png := accTestPNG(64)
	first, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same image gave different refs: %q and %q", first, second)
	}
	name, ok := strings.CutPrefix(first, "file:")
	if !ok || !regexp.MustCompile(`^[0-9a-f]{16}\.png$`).MatchString(name) {
		t.Fatalf("ref = %q, want file:<16 hex>.png", first)
	}
	if files := accTestIconFiles(t); len(files) != 1 || files[0] != name {
		t.Errorf("icons folder = %v, want exactly [%s]", files, name)
	}
	if IconFile(name) == "" {
		t.Errorf("IconFile(%q) should resolve the stored file", name)
	}
	if _, err := os.Stat(IconFile(name)); err != nil {
		t.Errorf("stored file missing: %v", err)
	}
}

func TestAccStoreIconRecognisesFormats(t *testing.T) {
	accTestIconHome(t)
	for want, data := range map[string][]byte{
		".svg":  []byte(`<?xml version="1.0"?><!-- logo --><svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		".ico":  append([]byte{0, 0, 1, 0}, bytes.Repeat([]byte{3}, 16)...),
		".gif":  []byte("GIF89a0000000000"),
		".jpg":  []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0},
		".webp": []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
	} {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Errorf("%s: %v", want, err)
			continue
		}
		if !strings.HasSuffix(ref, want) {
			t.Errorf("%s content stored as %q", want, ref)
		}
	}
	if _, err := StoreIcon([]byte("<html><body>not svg</body></html>")); err == nil {
		t.Error("html should not be accepted as svg")
	}
}

func TestAccIconFileOnlyAcceptsStoredNames(t *testing.T) {
	accTestIconHome(t)
	good := "0123456789abcdef.png"
	if got := IconFile(good); got == "" || filepath.Dir(got) != IconDir() {
		t.Errorf("IconFile(%q) = %q, want a path in the icons folder", good, got)
	}
	for _, bad := range []string{
		"",
		"../providers.json",
		"..\\providers.json",
		"sub/0123456789abcdef.png",
		"0123456789abcdef.exe",
		"0123456789abcdef.PNG",
		"0123456789ABCDEF.png",
		"0123456789abcde.png",
		"0123456789abcdef.png\n",
		"0123456789abcdef.png/..",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestAccIconValueResolvesInputs(t *testing.T) {
	accTestIconHome(t)
	dir := t.TempDir()
	png := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(png, accTestPNG(16), 0o644); err != nil {
		t.Fatal(err)
	}
	text := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(text, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := IconValue(""); v != "" || err != nil {
		t.Errorf(`IconValue("") = %q, %v`, v, err)
	}
	if v, err := IconValue("openai"); v != "openai" || err != nil {
		t.Errorf(`IconValue("openai") = %q, %v`, v, err)
	}
	if _, err := IconValue("Not A Name!"); err == nil {
		t.Error("an unknown name should be rejected")
	}
	if v, err := IconValue(png); err != nil || !strings.HasPrefix(v, "file:") {
		t.Errorf("IconValue(png) = %q, %v; want a file: ref", v, err)
	}
	if _, err := IconValue(text); err == nil {
		t.Error("a text file should be rejected")
	}
	if _, err := IconValue("file:../providers.json"); err == nil {
		t.Error("a file: reference outside the icons folder should be rejected")
	}
}

func TestAccSaveDropsOnlyOldUnreferencedIcons(t *testing.T) {
	accTestIconHome(t)
	old := time.Now().Add(-2 * time.Hour)

	kept, err := StoreIcon(accTestPNG(100))
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := StoreIcon(accTestPNG(200))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon(accTestPNG(300))
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{kept, dropped} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}

	if err := Save(Provider{Name: "Icon Test", Chat: "https://relay.example.com/v1", Key: "sk-test", Icon: kept}); err != nil {
		t.Fatal(err)
	}

	keptName := strings.TrimPrefix(kept, "file:")
	droppedName := strings.TrimPrefix(dropped, "file:")
	freshName := strings.TrimPrefix(fresh, "file:")
	have := map[string]bool{}
	for _, n := range accTestIconFiles(t) {
		have[n] = true
	}
	if !have[keptName] {
		t.Error("an icon a provider refers to was deleted")
	}
	if have[droppedName] {
		t.Error("an old icon nothing refers to should have been deleted")
	}
	if !have[freshName] {
		t.Error("an icon chosen within the last hour should be kept")
	}
}
