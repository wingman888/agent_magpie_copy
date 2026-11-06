package provider

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var storedIconRe = regexp.MustCompile(`^file:[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

func pngBytes(rest string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(rest)...)
}

func TestStoreIconKeepsOneCopyOfAPicture(t *testing.T) {
	isolate(t)
	img := pngBytes("one picture")
	v1, err := StoreIcon(img)
	if err != nil {
		t.Fatal(err)
	}
	if !storedIconRe.MatchString(v1) {
		t.Fatalf("icon value %q is not file:<16 hex>.png", v1)
	}
	v2, err := StoreIcon(img)
	if err != nil {
		t.Fatal(err)
	}
	if v1 != v2 {
		t.Fatalf("same picture stored twice: %q and %q", v1, v2)
	}
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("icons dir holds %d files, want 1", len(entries))
	}
	name := strings.TrimPrefix(v1, "file:")
	if path := IconFile(name); path == "" || filepath.Dir(path) != iconsDir() {
		t.Fatalf("IconFile(%q) = %q, want a file inside icons/", name, path)
	} else if _, err := os.Stat(path); err != nil {
		t.Fatalf("stored file missing: %v", err)
	}
}

func TestStoreIconRecognisesFormats(t *testing.T) {
	isolate(t)
	for want, data := range map[string][]byte{
		".png":  pngBytes("x"),
		".jpg":  append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, []byte("jfif")...),
		".gif":  []byte("GIF89a-data"),
		".webp": append(append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBP")...), []byte("VP8 ")...),
		".ico":  {0, 0, 1, 0, 1, 0},
		".svg":  []byte("  <?xml version=\"1.0\"?>\n<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"),
	} {
		v, err := StoreIcon(data)
		if err != nil {
			t.Fatalf("%s: %v", want, err)
		}
		if !strings.HasSuffix(v, want) {
			t.Errorf("%s stored as %q", want, v)
		}
	}
}

func TestStoreIconRejectsBadContent(t *testing.T) {
	isolate(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty content accepted")
	}
	if _, err := StoreIcon([]byte("just some plain text, not a picture")); err == nil {
		t.Error("plain text accepted")
	}
	if _, err := StoreIcon([]byte("<html><body>not svg</body></html>")); err == nil {
		t.Error("html accepted as an icon")
	}
	big := pngBytes(strings.Repeat("a", MaxIcon))
	if len(big) <= MaxIcon {
		t.Fatal("test picture is not over the limit")
	}
	if _, err := StoreIcon(big); err == nil {
		t.Error("icon over MaxIcon accepted")
	}
	atLimit := append(pngBytes(""), make([]byte, MaxIcon-8)...)
	if len(atLimit) != MaxIcon {
		t.Fatalf("test picture is %d bytes", len(atLimit))
	}
	if _, err := StoreIcon(atLimit); err != nil {
		t.Errorf("icon of exactly MaxIcon rejected: %v", err)
	}
}

func TestIconFileOnlyAcceptsStoredNames(t *testing.T) {
	isolate(t)
	for _, bad := range []string{
		"", "..", "../0123456789abcdef.png", "0123456789abcdef.png/../x", "a/0123456789abcdef.png",
		`..\0123456789abcdef.png`, "0123456789ABCDEF.png", "0123456789abcdef.exe", "0123456789abcdef",
		"0123456789abcdef.svg.exe", "file:0123456789abcdef.png", "/etc/passwd", "0123456789abcde.png",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
	good := "0123456789abcdef.webp"
	if got := IconFile(good); got != filepath.Join(iconsDir(), good) {
		t.Errorf("IconFile(%q) = %q", good, got)
	}
}

func TestSaveSweepsOldUnusedIcons(t *testing.T) {
	isolate(t)
	if err := os.MkdirAll(iconsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	unused := "00000000000000aa.png"
	used := "00000000000000bb.png"
	fresh := "00000000000000cc.png"
	for _, n := range []string{unused, used, fresh} {
		if err := os.WriteFile(filepath.Join(iconsDir(), n), pngBytes(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{unused, used} {
		if err := os.Chtimes(filepath.Join(iconsDir(), n), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := Save(Provider{ID: "keep", Name: "Keep", Chat: "https://keep.example/v1", Key: "k",
		Icon: "file:" + used}); err != nil {
		t.Fatal(err)
	}
	for n, want := range map[string]bool{unused: false, used: true, fresh: true} {
		_, err := os.Stat(filepath.Join(iconsDir(), n))
		if exists := err == nil; exists != want {
			t.Errorf("%s exists=%v, want %v", n, exists, want)
		}
	}
}
