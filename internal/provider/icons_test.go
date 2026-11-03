package provider

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// iconTestImage is magic-number-prefixed bytes padded with tag, so each
// test image has distinct content.
func iconTestImage(magic []byte, tag string) []byte {
	return append(append([]byte{}, magic...), []byte(tag)...)
}

var (
	iconPNG  = []byte("\x89PNG\r\n\x1a\n")
	iconJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0}
	iconGIF  = []byte("GIF89a")
	iconWEBP = []byte("RIFF\x00\x00\x00\x00WEBP")
	iconICO  = []byte{0x00, 0x00, 0x01, 0x00}
)

func iconsDirFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestStoreIconTypes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"png", iconTestImage(iconPNG, "a"), "png"},
		{"jpeg", iconTestImage(iconJPEG, "b"), "jpg"},
		{"gif", iconTestImage(iconGIF, "c"), "gif"},
		{"webp", iconTestImage(iconWEBP, "d"), "webp"},
		{"ico", iconTestImage(iconICO, "e"), "ico"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), "svg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, err := StoreIcon(c.data)
			if err != nil {
				t.Fatal(err)
			}
			name, ok := strings.CutPrefix(ref, "file:")
			if !ok || !strings.HasSuffix(name, "."+c.ext) {
				t.Fatalf("ref %q: want file: reference ending in .%s", ref, c.ext)
			}
			got, err := os.ReadFile(IconFile(name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, c.data) {
				t.Fatal("stored content differs from the original")
			}
		})
	}
}

func TestStoreIconRejects(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty data accepted")
	}
	if _, err := StoreIcon([]byte("just some plain text")); err == nil {
		t.Error("plain text accepted")
	}
	// The file name's extension is ignored: text named .png is still refused.
	if _, err := StoreIconFile(writeTemp(t, "logo.png", []byte("not an image"))); err == nil {
		t.Error("text file named .png accepted")
	}
	big := iconTestImage(iconPNG, "")
	big = append(big, make([]byte, MaxIcon+1-len(big))...)
	if _, err := StoreIcon(big); err == nil {
		t.Error("image over MaxIcon accepted")
	} else if !strings.Contains(err.Error(), "smaller") {
		t.Errorf("oversize error %q does not suggest a smaller image", err)
	}
	exact := iconTestImage(iconPNG, "")
	exact = append(exact, make([]byte, MaxIcon-len(exact))...)
	if _, err := StoreIcon(exact); err != nil {
		t.Errorf("image of exactly MaxIcon rejected: %v", err)
	}
	if n := len(iconsDirFiles(t)); n != 1 {
		t.Errorf("rejected images left files behind: %d files, want 1", n)
	}
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStoreIconFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	svg := writeTemp(t, "logo.txt", []byte(`<svg></svg>`))
	ref, err := StoreIconFile(svg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".svg") {
		t.Errorf("ref = %q, want file:...svg", ref)
	}
	if _, err := StoreIconFile(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Error("missing file accepted")
	}
	if _, err := StoreIconFile(writeTemp(t, "empty.png", nil)); err == nil {
		t.Error("empty file accepted")
	}
	if _, err := StoreIconFile(writeTemp(t, "huge.png", append(iconTestImage(iconPNG, ""), make([]byte, MaxIcon)...))); err == nil {
		t.Error("file over MaxIcon accepted")
	}
}

func TestStoreIconDuplicate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	data := iconTestImage(iconPNG, "same")
	first, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StoreIconFile(writeTemp(t, "copy.jpg", data))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("refs differ: %q vs %q", first, second)
	}
	if n := len(iconsDirFiles(t)); n != 1 {
		t.Errorf("icons dir has %d files, want 1", n)
	}
}

func TestIconFileNames(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	valid := "0123456789abcdef.png"
	if got := IconFile(valid); filepath.Base(got) != valid || filepath.Base(filepath.Dir(got)) != "icons" {
		t.Errorf("IconFile(%q) = %q, want path inside icons dir", valid, got)
	}
	for _, bad := range []string{
		"", "../providers.json", "x.png", "0123456789abcdef.exe",
		"0123456789ABCDEF.png", "0123456789abcdef.PNG", "0123456789abcde.png",
		"00123456789abcdef.png", "sub/0123456789abcdef.png", "..\\0123456789abcdef.png",
		"generic", "0123456789abcdef", "0123456789abcdef.png/..",
	} {
		if got := IconFile(bad); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", bad, got)
		}
	}
}

func TestIconFileReadsStored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	data := iconTestImage(iconGIF, "readback")
	ref, err := StoreIcon(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(IconFile(strings.TrimPrefix(ref, "file:")))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Error("IconFile did not point at the stored content")
	}
}

func setAge(t *testing.T, ref string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), at, at); err != nil {
		t.Fatal(err)
	}
}

func TestPruneIconsOneHourRule(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	used, err := StoreIcon(iconTestImage(iconPNG, "used"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := StoreIcon(iconTestImage(iconPNG, "old"))
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := StoreIcon(iconTestImage(iconPNG, "fresh"))
	if err != nil {
		t.Fatal(err)
	}
	// A file that is not a stored icon name is never touched.
	stray := filepath.Join(iconsDir(), "notes.txt")
	if err := os.WriteFile(stray, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stray, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Point a provider at the first image while everything is still fresh.
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k", Icon: used}); err != nil {
		t.Fatal(err)
	}
	long := time.Now().Add(-2 * time.Hour)
	setAge(t, used, long)
	setAge(t, old, long)

	// Saving another provider prunes: the referenced and fresh images stay,
	// the old unreferenced one goes.
	if err := Save(Provider{ID: "other", Name: "Other", Chat: "https://other.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{used, fresh} {
		if _, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:"))); err != nil {
			t.Errorf("%s removed: %v", ref, err)
		}
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(old, "file:"))); !os.IsNotExist(err) {
		t.Errorf("old unreferenced icon kept (err=%v)", err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("non-icon file removed: %v", err)
	}

	// Switching the provider to a built-in icon leaves the old image
	// unreferenced; once it is an hour old, the next save removes it.
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(used, "file:"))); !os.IsNotExist(err) {
		t.Errorf("unreferenced icon kept after switch to built-in (err=%v)", err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(fresh, "file:"))); err != nil {
		t.Errorf("fresh icon removed: %v", err)
	}
}

func TestPruneIconsKeepsRecentUnreferenced(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ref, err := StoreIcon(iconTestImage(iconPNG, "recent"))
	if err != nil {
		t.Fatal(err)
	}
	setAge(t, ref, time.Now().Add(-59*time.Minute))
	if err := pruneIcons(file{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:"))); err != nil {
		t.Errorf("icon under an hour old removed: %v", err)
	}
	if err := pruneIcons(file{}, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(IconFile(strings.TrimPrefix(ref, "file:"))); !os.IsNotExist(err) {
		t.Errorf("icon over an hour old kept (err=%v)", err)
	}
}
