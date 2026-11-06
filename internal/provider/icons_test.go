package provider

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func l1IcoPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestL1StoreIconPNGOnceAndNamed(t *testing.T) {
	l1IcoIsolateHome(t)
	data := l1IcoPNGBytes(t)
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	name, ok := strings.CutPrefix(ref, "file:")
	if !ok || !strings.HasSuffix(name, ".png") || len(name) != 16+len(".png") {
		t.Fatalf("ref = %q", ref)
	}
	again, err := StoreIcon(data)
	if err != nil || again != ref {
		t.Fatalf("second store = %q, %v", again, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(IconFile(name)))
	if len(entries) != 1 {
		t.Fatalf("same image stored %d times", len(entries))
	}
	if p := IconFile(name); p == "" {
		t.Fatal("stored name does not resolve")
	}
}

func TestL1StoreIconRejects(t *testing.T) {
	l1IcoIsolateHome(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty icon accepted")
	}
	if _, err := StoreIcon([]byte("just some text, not an image")); err == nil {
		t.Error("plain text accepted")
	}
	big := make([]byte, MaxIcon+1)
	copy(big, l1IcoPNGBytes(t))
	if _, err := StoreIcon(big); err == nil {
		t.Error("icon over 1 MB accepted")
	}
}

func TestL1StoreIconSVG(t *testing.T) {
	l1IcoIsolateHome(t)
	ref, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("svg ref = %q", ref)
	}
}

func TestL1IconFileOnlyAcceptsStoredNames(t *testing.T) {
	l1IcoIsolateHome(t)
	good := "0123456789abcdef.png"
	if p := IconFile(good); p != filepath.Join(filepath.Dir(Path()), "icons", good) {
		t.Errorf("IconFile(%q) = %q", good, p)
	}
	for _, bad := range []string{
		"", "../0123456789abcdef.png", "sub/0123456789abcdef.png", `..\0123456789abcdef.png`,
		"0123456789abcdef.exe", "0123456789ABCDEF.png", "0123456789abcde.png", "x.png", "0123456789abcdef.png.sh",
	} {
		if p := IconFile(bad); p != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, p)
		}
	}
}

func TestL1PruneIconsKeepsReferencedAndRecent(t *testing.T) {
	l1IcoIsolateHome(t)
	dir := iconsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	mk := func(name string, when time.Time) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	mk("aaaaaaaaaaaaaaaa.png", old)        // unreferenced, old: removed
	mk("bbbbbbbbbbbbbbbb.png", old)        // referenced, old: kept
	mk("cccccccccccccccc.png", time.Now()) // unreferenced, recent: kept
	pruneIcons([]Provider{{ID: "x", Icon: "file:bbbbbbbbbbbbbbbb.png"}})
	for name, want := range map[string]bool{"aaaaaaaaaaaaaaaa.png": false, "bbbbbbbbbbbbbbbb.png": true, "cccccccccccccccc.png": true} {
		_, err := os.Stat(filepath.Join(dir, name))
		if (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", name, err == nil, want)
		}
	}
}

func TestL1SaveKeepsReferencedIcon(t *testing.T) {
	l1IcoIsolateHome(t)
	ref, err := StoreIcon(l1IcoPNGBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	// referenced by the provider just saved: must survive the cleanup
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Icon: ref}); err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(ref, "file:")
	if _, err := os.Stat(IconFile(name)); err != nil {
		t.Fatalf("referenced icon removed: %v", err)
	}
}

// l1IcoIsolateHome points every magpie path at a fresh temp dir.
func l1IcoIsolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/cfg")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func TestL1SaveRejectsUnstoredIconReference(t *testing.T) {
	l1IcoIsolateHome(t)
	base := Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1"}
	bad := base
	bad.Icon = "file:../../etc/passwd"
	if err := Save(bad); err == nil {
		t.Fatal("Save accepted an icon reference that is not a stored icon")
	}
	ref, err := StoreIcon(l1IcoPNGBytes(t))
	if err != nil {
		t.Fatal(err)
	}
	good := base
	good.Icon = ref
	if err := Save(good); err != nil {
		t.Fatalf("Save rejected a stored icon: %v", err)
	}
}
