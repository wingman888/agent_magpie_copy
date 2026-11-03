package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateConfig points the config directory at a temp dir for one test.
func isolateConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

var (
	pngHead  = []byte("\x89PNG\r\n\x1a\n")
	jpegHead = []byte{0xFF, 0xD8, 0xFF, 0xE0}
	gifHead  = []byte("GIF89a")
	webpHead = []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
	icoHead  = []byte{0, 0, 1, 0, 1, 0}
	svgBody  = []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`)
)

func TestStoreIconNamesByContent(t *testing.T) {
	isolateConfig(t)
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", append(append([]byte{}, pngHead...), 1, 2, 3), ".png"},
		{"jpeg", jpegHead, ".jpg"},
		{"gif", gifHead, ".gif"},
		{"webp", webpHead, ".webp"},
		{"ico", icoHead, ".ico"},
		{"svg", svgBody, ".svg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := StoreIcon(c.data)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(c.data)
			want := "file:" + hex.EncodeToString(sum[:8]) + c.ext
			if ref != want {
				t.Fatalf("ref = %q, want %q", ref, want)
			}
			name := strings.TrimPrefix(ref, "file:")
			if _, err := os.Stat(IconFile(name)); err != nil {
				t.Fatalf("stored file missing: %v", err)
			}
		})
	}
}

func TestStoreIconDeduplicates(t *testing.T) {
	isolateConfig(t)
	a, err := StoreIcon(pngHead)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(append([]byte{}, pngHead...))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("same picture gave %q and %q", a, b)
	}
	entries, _ := os.ReadDir(iconsDir())
	if len(entries) != 1 {
		t.Fatalf("%d files stored, want 1", len(entries))
	}
}

func TestStoreIconRejects(t *testing.T) {
	isolateConfig(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty picture accepted")
	}
	big := append(append([]byte{}, pngHead...), make([]byte, MaxIcon)...)
	if _, err := StoreIcon(big); err == nil {
		t.Error("picture over 1 MB accepted")
	}
	exact := append(append([]byte{}, pngHead...), make([]byte, MaxIcon-len(pngHead))...)
	if len(exact) != MaxIcon {
		t.Fatalf("test setup: %d bytes", len(exact))
	}
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("exactly 1 MB refused: %v", err)
	}
	for _, text := range []string{"hello world", "<html><body>hi</body></html>", "<svg><script>alert(1)</script></svg>"} {
		if _, err := StoreIcon([]byte(text)); err == nil {
			t.Errorf("%q accepted as a picture", text)
		}
	}
}

func TestIconFileOnlyAcceptsOwnNames(t *testing.T) {
	isolateConfig(t)
	good := "0123456789abcdef.png"
	if got := IconFile(good); got == "" || filepath.Dir(got) != iconsDir() || filepath.Base(got) != good {
		t.Fatalf("IconFile(%q) = %q", good, got)
	}
	for _, name := range []string{
		"",
		"../0123456789abcdef.png",
		"..\\0123456789abcdef.png",
		"sub/0123456789abcdef.png",
		"0123456789abcdef.exe",
		"0123456789abcdef.PNG",
		"0123456789ABCDEF.png",
		"0123456789abcde.png",
		"0123456789abcdef0.png",
		"0123456789abcdef",
		"0123456789abcdef.png/..",
		"file:0123456789abcdef.png",
		"/etc/passwd",
		"providers.json",
	} {
		if got := IconFile(name); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", name, got)
		}
	}
}

func TestSaveRejectsForeignIconReference(t *testing.T) {
	isolateConfig(t)
	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk", Icon: "file:../../secret.png"}
	if err := Save(p); err == nil {
		t.Fatal("reference outside the icon store saved")
	}
}

func TestPruneIcons(t *testing.T) {
	isolateConfig(t)
	kept, err := StoreIcon(append(append([]byte{}, pngHead...), 'k'))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := StoreIcon(append(append([]byte{}, pngHead...), 's'))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon(append(append([]byte{}, pngHead...), 'f'))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{kept, stale} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}
	// Saving a provider that refers to `kept` sweeps the folder.
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk", Icon: kept}); err != nil {
		t.Fatal(err)
	}
	exists := func(ref string) bool {
		_, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:")))
		return err == nil
	}
	if !exists(kept) {
		t.Error("referenced picture removed")
	}
	if exists(stale) {
		t.Error("unreferenced picture older than an hour kept")
	}
	if !exists(fresh) {
		t.Error("unreferenced picture within the grace period removed")
	}
}

func TestPruneSkipsFolders(t *testing.T) {
	isolateConfig(t)
	if err := os.MkdirAll(iconsDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := iconsDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(sub, old, old); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatal("a folder inside icons/ was removed")
	}
	if !bytes.Contains(mustRead(t, Path()), []byte(`"relay"`)) {
		t.Fatal("provider not saved")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuiltinIcon(t *testing.T) {
	for _, name := range []string{"openai", "claude-color", "ollama"} {
		if !BuiltinIcon(name) {
			t.Errorf("%s is bundled but not recognised", name)
		}
	}
	for _, name := range []string{"", "nope", "file:x.png", "../openai"} {
		if BuiltinIcon(name) {
			t.Errorf("%q recognised as built-in", name)
		}
	}
}
