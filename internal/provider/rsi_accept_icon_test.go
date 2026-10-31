package provider_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

func isolate(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01")

func TestAcceptStoreIconFormats(t *testing.T) {
	isolate(t)
	cases := map[string][]byte{
		".png":  png,
		".jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1},
		".gif":  []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00"),
		".webp": []byte("RIFF\x24\x00\x00\x00WEBPVP8 \x18\x00\x00\x00"),
		".ico":  {0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 1, 0, 32, 0, 0, 0, 0, 0, 22, 0, 0, 0},
		".svg":  []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"/>`),
	}
	for ext, data := range cases {
		ref, err := provider.StoreIcon(data)
		if err != nil {
			t.Errorf("%s: %v", ext, err)
			continue
		}
		if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ext) {
			t.Errorf("%s: ref %q", ext, ref)
		}
		b, err := os.ReadFile(provider.IconFile(strings.TrimPrefix(ref, "file:")))
		if err != nil || string(b) != string(data) {
			t.Errorf("%s: content mismatch: %v", ext, err)
		}
	}
}

func TestAcceptStoreIconDedupAndFile(t *testing.T) {
	home := isolate(t)
	a, err := provider.StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(home, "logo.bin")
	os.WriteFile(src, png, 0o644)
	b, err := provider.StoreIconFile(src)
	if err != nil || a != b {
		t.Fatalf("file ref %q (%v), want %q", b, err, a)
	}
	os.Remove(src)
	p := provider.IconFile(strings.TrimPrefix(a, "file:"))
	if _, err := os.Stat(p); err != nil {
		t.Errorf("stored copy gone: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Dir(p))
	if len(ents) != 1 {
		t.Errorf("want one file in icon dir, got %d", len(ents))
	}
	if filepath.Dir(filepath.Dir(p)) != filepath.Join(home, ".config", "magpie") {
		t.Errorf("icon dir not beside providers.json: %s", p)
	}
}

func TestAcceptStoreIconRejects(t *testing.T) {
	home := isolate(t)
	if _, err := provider.StoreIcon([]byte("hello, I am text")); err == nil {
		t.Error("text accepted")
	}
	if _, err := provider.StoreIcon(nil); err == nil {
		t.Error("empty accepted")
	}
	big := append(append([]byte{}, png...), make([]byte, provider.MaxIcon)...)
	if _, err := provider.StoreIcon(big); err == nil {
		t.Error("oversize accepted")
	} else if !strings.Contains(strings.ToLower(err.Error()), "smaller") {
		t.Logf("oversize error: %v", err)
	}
	if provider.MaxIcon != 1<<20 {
		t.Errorf("MaxIcon = %d", provider.MaxIcon)
	}
	f := filepath.Join(home, "t.png")
	os.WriteFile(f, []byte("plain text named png"), 0o644)
	if _, err := provider.StoreIconFile(f); err == nil {
		t.Error("text file accepted")
	}
	if _, err := provider.StoreIconFile(filepath.Join(home, "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
}

func TestAcceptIconFileConfinement(t *testing.T) {
	isolate(t)
	for _, bad := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", "", "../icons/0123456789abcdef.png", "/etc/passwd"} {
		if got := provider.IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q", bad, got)
		}
	}
}

func TestAcceptIconPrune(t *testing.T) {
	isolate(t)
	custom := func(id, icon string) provider.Provider {
		return provider.Provider{ID: id, Name: id, Key: "k", Chat: "https://" + id + ".example.com/v1", Icon: icon}
	}
	oldRef, _ := provider.StoreIcon(png)
	freshRef, _ := provider.StoreIcon([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	if err := provider.Save(custom("a", oldRef)); err != nil {
		t.Fatal(err)
	}
	oldPath := provider.IconFile(strings.TrimPrefix(oldRef, "file:"))
	freshPath := provider.IconFile(strings.TrimPrefix(freshRef, "file:"))
	// referenced pictures survive, even when old
	past := time.Now().Add(-2 * time.Hour)
	os.Chtimes(oldPath, past, past)
	provider.Save(custom("b", "deepseek-color"))
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("referenced icon removed: %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh unreferenced icon removed: %v", err)
	}
	// switch a to a built-in icon: old picture goes at next save, fresh stays
	provider.Save(custom("a", "generic"))
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("unreferenced old icon kept: %v", err)
	}
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("fresh icon removed within the hour: %v", err)
	}
	// an old unreferenced picture is cleaned after delete too
	ref3, _ := provider.StoreIcon([]byte("GIF89a\x01\x00\x01\x00\x00\x00\x00"))
	provider.Save(custom("c", ref3))
	p3 := provider.IconFile(strings.TrimPrefix(ref3, "file:"))
	os.Chtimes(p3, past, past)
	provider.Delete("c")
	provider.Save(custom("d", ""))
	if _, err := os.Stat(p3); !os.IsNotExist(err) {
		t.Errorf("icon of deleted provider kept: %v", err)
	}
}
