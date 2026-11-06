package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// icnSetup points the config directory at a fresh temp dir.
func icnSetup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return dir
}

// icnPNG is a PNG-signed blob padded out to n bytes.
func icnPNG(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
	return b[:n]
}

// icnFiles lists the names in the icons directory.
func icnFiles(t *testing.T) []string {
	t.Helper()
	es, err := os.ReadDir(iconDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestIcnStoreSameContentOnce(t *testing.T) {
	icnSetup(t)
	data := icnPNG(100)
	ref1, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ref2 {
		t.Fatalf("refs differ: %q vs %q", ref1, ref2)
	}
	if len(ref1) != len("file:")+16+len(".png") || !strings.HasPrefix(ref1, "file:") || !strings.HasSuffix(ref1, ".png") {
		t.Fatalf("unexpected ref %q", ref1)
	}
	files := icnFiles(t)
	if len(files) != 1 || "file:"+files[0] != ref1 {
		t.Fatalf("icons dir = %v, want one file matching %q", files, ref1)
	}
	got, err := os.ReadFile(IconFile(ref1))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stored bytes differ from input (err=%v)", err)
	}
}

func TestIcnDetectsTypes(t *testing.T) {
	icnSetup(t)
	cases := map[string]struct {
		data []byte
		ext  string
	}{
		"jpeg": {append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("jfif")...), ".jpg"},
		"gif":  {[]byte("GIF89a0123456789"), ".gif"},
		"webp": {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ".webp"},
		"ico":  {[]byte{0x00, 0x00, 0x01, 0x00, 0x01, 0x00}, ".ico"},
		"svg":  {[]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"></svg>`), ".svg"},
	}
	for name, c := range cases {
		ref, err := StoreIcon(c.data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.HasSuffix(ref, c.ext) {
			t.Errorf("%s: ref %q, want suffix %s", name, ref, c.ext)
		}
	}
}

func TestIcnSVGAcceptedFromTxtFile(t *testing.T) {
	dir := t.TempDir()
	icnSetup(t)
	p := filepath.Join(dir, "logo.txt")
	if err := os.WriteFile(p, []byte(`<svg width="1"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(p)
	if err != nil {
		t.Fatalf("svg in .txt refused: %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("ref %q, want .svg", ref)
	}
}

func TestIcnRejects(t *testing.T) {
	icnSetup(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty data accepted")
	}
	if _, err := StoreIcon([]byte("just some plain text")); err == nil {
		t.Error("plain text accepted")
	}
	if _, err := StoreIcon(icnPNG(MaxIcon + 1)); err == nil {
		t.Error("data over MaxIcon accepted")
	}
	if _, err := StoreIcon(icnPNG(MaxIcon)); err != nil {
		t.Errorf("data of exactly MaxIcon refused: %v", err)
	}

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.png")
	os.WriteFile(empty, nil, 0o644)
	if _, err := StoreIconFile(empty); err == nil {
		t.Error("empty file accepted")
	}
	big := filepath.Join(dir, "big.png")
	os.WriteFile(big, icnPNG(MaxIcon+1), 0o644)
	if _, err := StoreIconFile(big); err == nil {
		t.Error("big file accepted")
	}
	if _, err := StoreIconFile(filepath.Join(dir, "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestIcnIconFileNames(t *testing.T) {
	icnSetup(t)
	for _, bad := range []string{"", "../providers.json", "x.png", "0123456789abcdef.exe", "0123456789ABCDEF.png", "../0123456789abcdef.png", "0123456789abcdef.PNG"} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
	good := "0123456789abcdef.png"
	if got := IconFile(good); got != filepath.Join(iconDir(), good) {
		t.Errorf("IconFile(%q) = %q", good, got)
	}
	if got := IconFile("file:" + good); got == "" {
		t.Errorf("IconFile with file: prefix returned empty")
	}
}

func TestIcnPruneOldUnreferenced(t *testing.T) {
	icnSetup(t)
	old := time.Now().Add(-2 * time.Hour)

	kept, err := StoreIcon(icnPNG(50))
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := StoreIcon([]byte("GIF89a-dropped"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon([]byte("GIF89a-fresh"))
	if err != nil {
		t.Fatal(err)
	}
	os.Chtimes(IconFile(kept), old, old)
	os.Chtimes(IconFile(dropped), old, old)

	if err := Save(Provider{Name: "Relay", Chat: "https://relay.test/v1", Key: "k", Icon: kept}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(kept)); err != nil {
		t.Errorf("referenced old icon removed: %v", err)
	}
	if _, err := os.Stat(IconFile(dropped)); !os.IsNotExist(err) {
		t.Errorf("unreferenced old icon kept (err=%v)", err)
	}
	if _, err := os.Stat(IconFile(fresh)); err != nil {
		t.Errorf("fresh icon removed: %v", err)
	}

	// switching the provider to a built-in name releases the stored image
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.test/v1", Key: "k", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(kept)); !os.IsNotExist(err) {
		t.Errorf("old icon kept after switching to built-in (err=%v)", err)
	}
}

func TestIcnDeletePrunes(t *testing.T) {
	icnSetup(t)
	ref, err := StoreIcon(icnPNG(60))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.test/v1", Key: "k", Icon: ref}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(IconFile(ref), old, old)
	if err := Delete("relay"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(ref)); !os.IsNotExist(err) {
		t.Errorf("icon of deleted provider kept (err=%v)", err)
	}
}
