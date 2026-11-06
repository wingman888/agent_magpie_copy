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

// icnSandbox points the config (and so the icons directory) and HOME at a
// fresh temp dir for one test.
func icnSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

var icnPNG = append([]byte("\x89PNG\r\n\x1a\n"), []byte("fake png body")...)

func icnCountIcons(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(iconsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	return len(entries)
}

func icnAge(t *testing.T, path string, d time.Duration) {
	t.Helper()
	old := time.Now().Add(-d)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func TestStoreIconIdempotent(t *testing.T) {
	icnSandbox(t)
	ref1, err := StoreIcon(icnPNG)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := StoreIcon(icnPNG)
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ref2 {
		t.Fatalf("same content gave different refs: %q vs %q", ref1, ref2)
	}
	if !regexp.MustCompile(`^file:[0-9a-f]{16}\.png$`).MatchString(ref1) {
		t.Fatalf("unexpected ref %q", ref1)
	}
	if n := icnCountIcons(t); n != 1 {
		t.Fatalf("want 1 file in icons dir, got %d", n)
	}
	path := IconFile(strings.TrimPrefix(ref1, "file:"))
	if path == "" {
		t.Fatal("IconFile returned empty for a stored icon")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, icnPNG) {
		t.Fatalf("stored content differs from original (err=%v)", err)
	}
	if filepath.Dir(path) != iconsDir() {
		t.Fatalf("icon stored outside icons dir: %s", path)
	}
}

func TestIconFileRejectsBadNames(t *testing.T) {
	icnSandbox(t)
	ref, err := StoreIcon(icnPNG)
	if err != nil {
		t.Fatal(err)
	}
	// make sure the names below would otherwise point at something real
	if err := os.WriteFile(filepath.Join(iconsDir(), "0123456789abcdef.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"../providers.json",
		"x.png",
		"0123456789abcdef.exe",
		"",
		"../icons/" + strings.TrimPrefix(ref, "file:"),
		"0123456789ABCDEF.png",
		"0123456789abcdef0.png",
	} {
		if got := IconFile(name); got != "" {
			t.Errorf("IconFile(%q) = %q, want empty", name, got)
		}
	}
}

func TestStoreIconFileSVGWithTxtExtension(t *testing.T) {
	home := icnSandbox(t)
	src := filepath.Join(home, "logo.txt")
	if err := os.WriteFile(src, []byte(`<?xml version="1.0"?><SVG xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := StoreIconFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("want an .svg ref, got %q", ref)
	}
}

func TestStoreIconFileMissing(t *testing.T) {
	home := icnSandbox(t)
	if _, err := StoreIconFile(filepath.Join(home, "nope.png")); err == nil {
		t.Fatal("missing file accepted")
	}
}

func TestStoreIconLimitsAndTypes(t *testing.T) {
	icnSandbox(t)

	if _, err := StoreIcon(nil); err == nil {
		t.Error("empty data accepted")
	}

	big := make([]byte, MaxIcon+1)
	copy(big, icnPNG)
	_, err := StoreIcon(big)
	if err == nil || !strings.Contains(err.Error(), "smaller") {
		t.Errorf("oversized image: want error mentioning smaller, got %v", err)
	}

	atLimit := make([]byte, MaxIcon)
	copy(atLimit, icnPNG)
	if _, err := StoreIcon(atLimit); err != nil {
		t.Errorf("image of exactly MaxIcon bytes rejected: %v", err)
	}

	if _, err := StoreIcon([]byte("just some plain text")); err == nil {
		t.Error("plain text accepted")
	}

	for _, c := range []struct {
		data []byte
		ext  string
	}{
		{[]byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2}, ".jpg"},
		{[]byte("GIF89a....."), ".gif"},
		{append([]byte("RIFF\x00\x00\x00\x00WEBP"), 1, 2), ".webp"},
		{[]byte{0, 0, 1, 0, 9, 9}, ".ico"},
		{[]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), ".svg"},
	} {
		ref, err := StoreIcon(c.data)
		if err != nil {
			t.Errorf("%s: %v", c.ext, err)
			continue
		}
		if !strings.HasSuffix(ref, c.ext) {
			t.Errorf("want %s ref, got %q", c.ext, ref)
		}
	}
}

func TestPruneIcons(t *testing.T) {
	icnSandbox(t)

	usedRef, err := StoreIcon(icnPNG)
	if err != nil {
		t.Fatal(err)
	}
	staleRef, err := StoreIcon([]byte("GIF89a stale"))
	if err != nil {
		t.Fatal(err)
	}
	freshRef, err := StoreIcon([]byte(`<svg id="fresh"/>`))
	if err != nil {
		t.Fatal(err)
	}
	icnAge(t, IconFile(strings.TrimPrefix(usedRef, "file:")), 2*time.Hour)
	icnAge(t, IconFile(strings.TrimPrefix(staleRef, "file:")), 2*time.Hour)

	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Icon: usedRef}); err != nil {
		t.Fatal(err)
	}
	// Saving another provider triggers the cleanup.
	if err := Save(Provider{ID: "other", Name: "Other", Chat: "https://other.example.com/v1", Key: "k", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}

	if IconFile(strings.TrimPrefix(staleRef, "file:")) != "" {
		t.Error("old unreferenced icon was not cleaned up")
	}
	if IconFile(strings.TrimPrefix(usedRef, "file:")) == "" {
		t.Error("old icon still referenced by a provider was removed")
	}
	if IconFile(strings.TrimPrefix(freshRef, "file:")) == "" {
		t.Error("recently stored icon was removed")
	}

	// Deleting the provider that used the image makes it eligible once it is old.
	if err := Delete("relay"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "third", Name: "Third", Chat: "https://third.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if IconFile(strings.TrimPrefix(usedRef, "file:")) != "" {
		t.Error("icon of a deleted provider was not cleaned up")
	}
	if IconFile(strings.TrimPrefix(freshRef, "file:")) == "" {
		t.Error("recently stored icon was removed after Delete")
	}
}

func TestCleanHeaders(t *testing.T) {
	icnSandbox(t)

	if err := Save(Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k",
		Headers: map[string]string{"  X-Org-Id ": "  acme  ", "   ": "dropped", "X-ORG-id": "kept"},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Org-Id": "acme", "X-ORG-id": "kept"}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}

	preset := normalize(Provider{ID: "anthropic", Preset: "anthropic", Key: "k", Headers: map[string]string{"X-A": "b"}})
	if preset.Headers != nil {
		t.Errorf("preset kept headers: %v", preset.Headers)
	}
}
