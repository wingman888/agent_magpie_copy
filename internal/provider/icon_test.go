package provider

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateConfig points the config and home directories at temp dirs so the
// tests never touch the real providers.json, icons, or signed-in agents.
func isolateConfig(t *testing.T) string {
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	return filepath.Join(cfg, "magpie", "icons")
}

func pngImage(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const svgDoc = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><circle r="1"/></svg>`

func iconNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestStoreIconAcceptsPNGAndSVG(t *testing.T) {
	dir := isolateConfig(t)

	ref, err := StoreIcon(pngImage(t))
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	name, ok := strings.CutPrefix(ref, "file:")
	if !ok || !strings.HasSuffix(name, ".png") || len(name) != 16+len(".png") {
		t.Fatalf("png ref = %q, want file:<16 hex>.png", ref)
	}
	if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
		t.Fatalf("png not stored: %v", err)
	}

	ref, err = StoreIcon([]byte(svgDoc))
	if err != nil {
		t.Fatalf("svg: %v", err)
	}
	if !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("svg ref = %q", ref)
	}
	// an svg without an xml prolog is an svg too
	if _, err := StoreIcon([]byte(`<!-- logo --><svg viewBox="0 0 1 1"/>`)); err != nil {
		t.Fatalf("bare svg: %v", err)
	}
}

func TestStoreIconDetectsFormatsByContent(t *testing.T) {
	isolateConfig(t)
	cases := map[string][]byte{
		".jpg":  {0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10},
		".gif":  []byte("GIF89a\x01\x00\x01\x00"),
		".webp": []byte("RIFF\x10\x00\x00\x00WEBPVP8 "),
		".ico":  {0, 0, 1, 0, 1, 0},
	}
	for ext, data := range cases {
		ref, err := StoreIcon(data)
		if err != nil {
			t.Fatalf("%s: %v", ext, err)
		}
		if !strings.HasSuffix(ref, ext) {
			t.Errorf("%s: ref = %q", ext, ref)
		}
	}
}

func TestStoreIconRejects(t *testing.T) {
	isolateConfig(t)
	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty content accepted")
	}
	if _, err := StoreIcon([]byte("just some plain text")); err == nil {
		t.Error("plain text accepted")
	}
	if _, err := StoreIcon([]byte("<html><body>not an svg</body></html>")); err == nil {
		t.Error("html without an svg root accepted")
	}
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxIcon)...)
	if len(big) <= MaxIcon {
		t.Fatal("test image not over the limit")
	}
	if _, err := StoreIcon(big); err == nil {
		t.Error("image over 1 MB accepted")
	}
}

func TestStoreIconStoresDuplicateOnce(t *testing.T) {
	dir := isolateConfig(t)
	a, err := StoreIcon([]byte(svgDoc))
	if err != nil {
		t.Fatal(err)
	}
	b, err := StoreIcon([]byte(svgDoc))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("same image gave %q and %q", a, b)
	}
	if n := iconNames(t, dir); len(n) != 1 {
		t.Errorf("icons folder holds %v, want one file", n)
	}
}

func TestIconFileOnlyAcceptsStoredNames(t *testing.T) {
	dir := isolateConfig(t)
	ref, err := StoreIcon(pngImage(t))
	if err != nil {
		t.Fatal(err)
	}
	name := strings.TrimPrefix(ref, "file:")
	if got := IconFile(name); got != filepath.Join(dir, name) {
		t.Errorf("IconFile(%q) = %q", name, got)
	}
	bad := []string{
		"",
		"../" + name,
		"..\\" + name,
		"sub/" + name,
		name + "/..",
		"0123456789abcdef.exe",
		"0123456789abcdef.PNG",
		"0123456789ABCDEF.png",
		"0123.png",
		"0123456789abcdef.png.png",
		"file:" + name,
		"/etc/passwd",
	}
	for _, b := range bad {
		if got := IconFile(b); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", b, got)
		}
	}
}

func TestResolveIconFileBuiltinAndEmpty(t *testing.T) {
	isolateConfig(t)
	src := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(src, pngImage(t), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := ResolveIcon(src)
	if err != nil || !strings.HasPrefix(v, "file:") {
		t.Fatalf("ResolveIcon(file) = %q, %v", v, err)
	}
	if v, err := ResolveIcon("openai"); err != nil || v != "openai" {
		t.Errorf("ResolveIcon(builtin) = %q, %v", v, err)
	}
	if v, err := ResolveIcon(""); err != nil || v != "" {
		t.Errorf("ResolveIcon(\"\") = %q, %v", v, err)
	}
	if _, err := ResolveIcon("no-such-logo"); err == nil {
		t.Error("unknown name accepted")
	}
	text := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(text, []byte("plain text, not an image"), 0o644)
	if _, err := ResolveIcon(text); err == nil {
		t.Error("text file accepted as icon")
	}
}

func TestSetIconRefusesPresetAndAccepts(t *testing.T) {
	isolateConfig(t)
	pr, err := FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test-preset"
	if err := Save(pr); err != nil {
		t.Fatal(err)
	}
	err = SetIcon("deepseek", "openai")
	if err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置") {
		t.Fatalf("preset icon error = %v", err)
	}

	custom := Provider{Name: "My Relay", Chat: "https://relay.example.com/v1", Key: "sk-custom"}
	if err := Save(custom); err != nil {
		t.Fatal(err)
	}
	if err := SetIcon("my-relay", "kimi"); err != nil {
		t.Fatalf("builtin icon: %v", err)
	}
	p, err := Find("my-relay")
	if err != nil || p.Icon != "kimi" {
		t.Fatalf("after builtin: icon %q, %v", p.Icon, err)
	}
	if err := SetIcon("my-relay", "file:0123456789abcdef.png"); err == nil {
		t.Error("reference to a missing icon accepted")
	}
	if err := SetIcon("my-relay", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if p, _ := Find("my-relay"); p.Icon != "" {
		t.Errorf("cleared icon = %q", p.Icon)
	}
	if err := SetIcon("missing", "kimi"); err == nil {
		t.Error("unknown provider accepted")
	}
}

func TestSaveKeepsReferencedIconsAndPrunesOld(t *testing.T) {
	dir := isolateConfig(t)
	refA, err := StoreIcon(pngImage(t))
	if err != nil {
		t.Fatal(err)
	}
	refB, err := StoreIcon([]byte(svgDoc))
	if err != nil {
		t.Fatal(err)
	}
	refC, err := StoreIcon([]byte(`<svg viewBox="0 0 2 2"/>`))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, ref := range []string{refA, refB} {
		if err := os.Chtimes(IconFile(strings.TrimPrefix(ref, "file:")), old, old); err != nil {
			t.Fatal(err)
		}
	}

	// A references refA; B is old and unreferenced; C is fresh and unreferenced.
	if err := Save(Provider{Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Icon: refA}); err != nil {
		t.Fatal(err)
	}

	have := map[string]bool{}
	for _, n := range iconNames(t, dir) {
		have[n] = true
	}
	nameA := strings.TrimPrefix(refA, "file:")
	nameB := strings.TrimPrefix(refB, "file:")
	nameC := strings.TrimPrefix(refC, "file:")
	if !have[nameA] {
		t.Error("referenced icon was deleted")
	}
	if have[nameB] {
		t.Error("old unreferenced icon was kept")
	}
	if !have[nameC] {
		t.Error("fresh unreferenced icon (inside the grace period) was deleted")
	}
}

func TestPruneLeavesNonIconFiles(t *testing.T) {
	dir := isolateConfig(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "README.txt")
	os.WriteFile(stray, []byte("not an icon"), 0o644)
	old := time.Now().Add(-5 * time.Hour)
	os.Chtimes(stray, old, old)
	pruneIcons(map[string]bool{}, time.Now())
	if _, err := os.Stat(stray); err != nil {
		t.Errorf("non-icon file removed: %v", err)
	}
}
