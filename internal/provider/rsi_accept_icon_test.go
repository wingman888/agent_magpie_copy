package provider_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

var (
	pngData  = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	jpgData  = []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00\x01")
	gifData  = []byte("GIF89a\x01\x00\x01\x00")
	webpData = []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00")
	icoData  = []byte("\x00\x00\x01\x00\x01\x00\x10\x10\x00\x00\x01\x00\x20\x00\x68\x04\x00\x00\x16\x00\x00\x00")
)

func TestAcceptStoreIcon(t *testing.T) {
	isolate(t)
	re := regexp.MustCompile(`^file:[0-9a-f]{16}\.png$`)
	ref, err := provider.StoreIcon(pngData)
	if err != nil {
		t.Fatal(err)
	}
	if !re.MatchString(ref) {
		t.Fatalf("ref = %q", ref)
	}
	if again, _ := provider.StoreIcon(pngData); again != ref {
		t.Errorf("not deterministic: %q %q", ref, again)
	}
	path := provider.IconFile(strings.TrimPrefix(ref, "file:"))
	if b, err := os.ReadFile(path); err != nil || !bytes.Equal(b, pngData) {
		t.Fatalf("IconFile %q: %v", path, err)
	}
	if filepath.Dir(path) != provider.IconDir() || filepath.Dir(provider.IconDir()) != filepath.Dir(provider.Path()) {
		t.Errorf("icon dir %q vs providers %q", provider.IconDir(), provider.Path())
	}
	if filepath.Base(provider.IconDir()) != "icons" {
		t.Errorf("icon dir = %q", provider.IconDir())
	}
	if st, err := os.Stat(path); err == nil && st.Mode().Perm() != 0o644 {
		t.Errorf("icon mode %v", st.Mode().Perm())
	}
}

func TestAcceptStoreIconFormats(t *testing.T) {
	isolate(t)
	cases := map[string][]byte{
		"png":  pngData,
		"jpg":  jpgData,
		"gif":  gifData,
		"webp": webpData,
		"ico":  icoData,
		"svg":  []byte(`<?xml version="1.0"?><SVG xmlns="http://www.w3.org/2000/svg"/>`),
	}
	for ext, data := range cases {
		ref, err := provider.StoreIcon(data)
		if err != nil {
			t.Errorf("%s: %v", ext, err)
			continue
		}
		if !strings.HasSuffix(ref, "."+ext) || !strings.HasPrefix(ref, "file:") {
			t.Errorf("%s: ref %q", ext, ref)
		}
	}
}

func TestAcceptStoreIconErrors(t *testing.T) {
	isolate(t)
	if _, err := provider.StoreIcon([]byte("hello")); err == nil || err.Error() != "not a picture magpie can show: use PNG, JPEG, GIF, WebP, ICO or SVG" {
		t.Errorf("hello: %v", err)
	}
	if _, err := provider.StoreIcon([]byte{}); err == nil || err.Error() != "the picture is empty" {
		t.Errorf("empty: %v", err)
	}
	big := append(append([]byte{}, pngData...), make([]byte, provider.MaxIcon+1-len(pngData))...)
	if _, err := provider.StoreIcon(big); err == nil || err.Error() != "the picture is over 1 MB; pick a smaller one" {
		t.Errorf("big: %v", err)
	}
	exact := append(append([]byte{}, pngData...), make([]byte, provider.MaxIcon-len(pngData))...)
	if _, err := provider.StoreIcon(exact); err != nil {
		t.Errorf("exact MaxIcon: %v", err)
	}
	// file source
	dir := t.TempDir()
	f := filepath.Join(dir, "big.png")
	os.WriteFile(f, big, 0o644)
	if _, err := provider.StoreIconFile(f); err == nil || err.Error() != "the picture is over 1 MB; pick a smaller one" {
		t.Errorf("big file: %v", err)
	}
	g := filepath.Join(dir, "ok.png")
	os.WriteFile(g, pngData, 0o644)
	ref, err := provider.StoreIconFile(g)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := provider.StoreIcon(pngData); ref != want {
		t.Errorf("file ref %q != %q", ref, want)
	}
}

func TestAcceptIconFileRejects(t *testing.T) {
	isolate(t)
	for _, bad := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", "", "0123456789ABCDEF.png", "../0123456789abcdef.png"} {
		if got := provider.IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q", bad, got)
		}
	}
	if provider.IconFile("0123456789abcdef.png") == "" {
		t.Error("valid name rejected")
	}
}

func TestAcceptIconPrune(t *testing.T) {
	isolate(t)
	ref, err := provider.StoreIcon(pngData)
	if err != nil {
		t.Fatal(err)
	}
	dir := provider.IconDir()
	old := time.Now().Add(-3 * time.Hour)
	mk := func(name string, mt time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, mt, mt)
		return p
	}
	orphanOld := mk("aaaaaaaaaaaaaaaa.png", old)
	orphanNew := mk("bbbbbbbbbbbbbbbb.png", time.Now().Add(-5*time.Minute))
	stranger := mk("notes.txt", old)
	usedPath := provider.IconFile(strings.TrimPrefix(ref, "file:"))
	os.Chtimes(usedPath, old, old)

	err = provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk", Chat: "https://relay.example.com/v1", Icon: ref})
	if err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists(orphanOld) {
		t.Error("old unreferenced icon kept")
	}
	if !exists(orphanNew) {
		t.Error("recent icon removed")
	}
	if !exists(usedPath) {
		t.Error("referenced icon removed")
	}
	if !exists(stranger) {
		t.Error("non-icon file removed")
	}
	if st, _ := os.Stat(provider.Path()); st.Mode().Perm() != 0o600 {
		t.Errorf("providers.json mode %v", st.Mode().Perm())
	}
}
