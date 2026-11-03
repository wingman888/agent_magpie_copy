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

var iconRefRe = regexp.MustCompile(`^file:[0-9a-f]{16}\.(png|jpg|gif|webp|ico|svg)$`)

// setupIcons points config at a fresh temp dir and pins the clock.
func setupIcons(t *testing.T) time.Time {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	old := nowFunc
	t0 := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return t0 }
	t.Cleanup(func() { nowFunc = old })
	return t0
}

func pngBytes(n int) []byte {
	b := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, n)...)
	return b
}

func iconEntries(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(iconDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestStoreIconMagicNumbers(t *testing.T) {
	setupIcons(t)
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", pngBytes(16), ".png"},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10}, ".jpg"},
		{"gif87", []byte("GIF87a....."), ".gif"},
		{"gif89", []byte("GIF89a....."), ".gif"},
		{"webp", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), ".webp"},
		{"ico", []byte{0, 0, 1, 0, 1, 0}, ".ico"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), ".svg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := StoreIcon(c.data)
			if err != nil {
				t.Fatal(err)
			}
			if !iconRefRe.MatchString(ref) || !strings.HasSuffix(ref, c.ext) {
				t.Fatalf("ref %q, want file:<16 hex>%s", ref, c.ext)
			}
			got, err := os.ReadFile(IconFile(ref))
			if err != nil || !bytes.Equal(got, c.data) {
				t.Fatalf("stored content mismatch: %v", err)
			}
		})
	}
}

func TestStoreIconSVGWithTxtExtension(t *testing.T) {
	setupIcons(t)
	src := filepath.Join(t.TempDir(), "logo.txt")
	if err := os.WriteFile(src, []byte(`<?xml version="1.0"?><svg width="1"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("ref %q, want .svg", ref)
	}
}

func TestStoreIconRejectsPlainText(t *testing.T) {
	setupIcons(t)
	if _, err := StoreIcon([]byte("just some text, not an image")); err == nil {
		t.Fatal("plain text accepted")
	}
	if len(iconEntries(t)) != 0 {
		t.Fatal("rejected icon left a file behind")
	}
}

func TestStoreIconRejectsEmpty(t *testing.T) {
	setupIcons(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Fatal("empty data accepted")
	}
	if _, err := StoreIcon([]byte{}); err == nil {
		t.Fatal("empty slice accepted")
	}
}

func TestStoreIconSizeLimit(t *testing.T) {
	setupIcons(t)
	if _, err := StoreIcon(pngBytes(MaxIcon - 8 + 1)); err == nil {
		t.Fatal("data over 1 MB accepted")
	} else if !strings.Contains(err.Error(), "smaller") {
		t.Fatalf("error should suggest a smaller image: %v", err)
	}
	ref, err := StoreIcon(pngBytes(MaxIcon - 8))
	if err != nil {
		t.Fatalf("exactly 1 MB rejected: %v", err)
	}
	if fi, _ := os.Stat(IconFile(ref)); fi == nil || fi.Size() != MaxIcon {
		t.Fatalf("stored size wrong: %v", fi)
	}
}

func TestStoreIconFileSizeLimit(t *testing.T) {
	setupIcons(t)
	big := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(big, pngBytes(MaxIcon), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StoreIconFile(big); err == nil {
		t.Fatal("oversized file accepted")
	}
	missing := filepath.Join(t.TempDir(), "nope.png")
	if _, err := StoreIconFile(missing); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestStoreIconSameContentOneFile(t *testing.T) {
	setupIcons(t)
	data := pngBytes(64)
	a, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon(append([]byte(nil), data...))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("refs differ: %q %q", a, b)
	}
	if n := len(iconEntries(t)); n != 1 {
		t.Fatalf("%d files in icons dir, want 1", n)
	}
}

func TestStoreIconRefreshesMtime(t *testing.T) {
	t0 := setupIcons(t)
	ref, err := StoreIcon(pngBytes(32))
	if err != nil {
		t.Fatal(err)
	}
	later := t0.Add(30 * time.Minute)
	nowFunc = func() time.Time { return later }
	if _, err := StoreIcon(pngBytes(32)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(IconFile(ref))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(later) {
		t.Fatalf("mtime %v, want %v", fi.ModTime(), later)
	}
}

func TestIconFileRejectsBadNames(t *testing.T) {
	setupIcons(t)
	for _, name := range []string{
		"../providers.json",
		"x.png",
		"0123456789abcdef.exe",
		"",
		"file:",
		"file:../providers.json",
		"0123456789ABCDEF.png",
		"0123456789abcdef0.png",
		"sub/0123456789abcdef.png",
		"0123456789abcdef.PNG",
		"generic",
	} {
		if got := IconFile(name); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", name, got)
		}
	}
}

func TestIconFileReturnsStoredContent(t *testing.T) {
	setupIcons(t)
	data := []byte("GIF89a-content")
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{ref, strings.TrimPrefix(ref, "file:")} {
		p := IconFile(name)
		if p == "" {
			t.Fatalf("IconFile(%q) empty", name)
		}
		got, err := os.ReadFile(p)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("content mismatch for %q: %v", name, err)
		}
	}
}

func TestIconDirNextToProviders(t *testing.T) {
	setupIcons(t)
	want := filepath.Join(filepath.Dir(Path()), "icons")
	if iconDir() != want {
		t.Fatalf("iconDir %q, want %q", iconDir(), want)
	}
}

func TestGCRemovesUnreferencedOldIcons(t *testing.T) {
	t0 := setupIcons(t)
	keep, err := StoreIcon(pngBytes(10))
	if err != nil {
		t.Fatal(err)
	}
	drop, err := StoreIcon(pngBytes(20))
	if err != nil {
		t.Fatal(err)
	}
	relay := Provider{ID: "relay", Name: "Relay", Key: "sk-x", Chat: "https://relay.example/v1", Icon: keep}
	if err := Save(relay); err != nil {
		t.Fatal(err)
	}
	// Both are fresh, so a save inside the grace period keeps everything.
	if err := Save(relay); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(drop)); err != nil {
		t.Fatalf("fresh unreferenced icon removed too early: %v", err)
	}

	// Two hours later the unreferenced icon is collected on the next save.
	nowFunc = func() time.Time { return t0.Add(2 * time.Hour) }
	if err := Save(relay); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(drop)); !os.IsNotExist(err) {
		t.Fatalf("old unreferenced icon kept: %v", err)
	}
	if _, err := os.Stat(IconFile(keep)); err != nil {
		t.Fatalf("referenced icon removed: %v", err)
	}
}

func TestGCAfterIconChangeAndDelete(t *testing.T) {
	t0 := setupIcons(t)
	old, err := StoreIcon(pngBytes(11))
	if err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "relay", Name: "Relay", Key: "sk-x", Chat: "https://relay.example/v1", Icon: old}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}

	// Later, the icon is replaced by a built-in name: the old file is now unused.
	nowFunc = func() time.Time { return t0.Add(2 * time.Hour) }
	fresh, err := StoreIcon(pngBytes(12))
	if err != nil {
		t.Fatal(err)
	}
	p.Icon = "deepseek-color"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(old)); !os.IsNotExist(err) {
		t.Fatalf("replaced icon not collected: %v", err)
	}
	// The freshly stored icon is inside the grace period and must survive.
	if _, err := os.Stat(IconFile(fresh)); err != nil {
		t.Fatalf("fresh icon collected: %v", err)
	}

	// Deleting the provider leaves the fresh icon alone, but it is collected
	// once it is over an hour old and a later save runs.
	p.Icon = fresh
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if err := Delete("relay"); err != nil {
		t.Fatal(err)
	}
	nowFunc = func() time.Time { return t0.Add(4 * time.Hour) }
	if err := Save(Provider{ID: "other", Name: "Other", Key: "sk-y", Chat: "https://other.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(fresh)); !os.IsNotExist(err) {
		t.Fatalf("unreferenced icon kept after grace period: %v", err)
	}
}
