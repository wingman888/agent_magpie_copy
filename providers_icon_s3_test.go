package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// icoS3Env points config (and so the icons dir) at a fresh temp dir and
// returns the icons dir.
func icoS3Env(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	return filepath.Join(filepath.Dir(provider.Path()), "icons")
}

// icoS3Write writes data to a file under dir and returns its path.
func icoS3Write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// icoS3Icon returns the stored icon value of the provider with id.
func icoS3Icon(t *testing.T, id string) string {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Icon
}

func TestIconS3AddStoresFileAndDedupes(t *testing.T) {
	icons := icoS3Env(t)
	src := t.TempDir()
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("logo-bytes")...)
	logo := icoS3Write(t, src, "logo.png", png)

	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://relay.example.com/v1", "key=sk", "icon=" + logo}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Fatalf("icon = %q, want file:*.png", p.Icon)
	}
	var again provider.Provider
	copyPath := icoS3Write(t, src, "copy.png", png)
	if err := applyPairs(&again, []string{"url=https://relay.example.com/v1", "key=sk", "icon=" + copyPath}); err != nil {
		t.Fatal(err)
	}
	if again.Icon != p.Icon {
		t.Errorf("same image gave %q then %q", p.Icon, again.Icon)
	}
	entries, err := os.ReadDir(icons)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("icons dir has %d files, want 1", len(entries))
	}
}

func TestIconS3CommandSetsAndResets(t *testing.T) {
	icons := icoS3Env(t)
	if err := provider.Save(provider.Provider{ID: "icos3-relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}
	logo := icoS3Write(t, t.TempDir(), "logo.png", append([]byte("\x89PNG\r\n\x1a\n"), []byte("cmd")...))

	if err := providerCmd([]string{"provider", "icon", "icos3-relay", logo}); err != nil {
		t.Fatal(err)
	}
	ref := icoS3Icon(t, "icos3-relay")
	if !strings.HasPrefix(ref, "file:") {
		t.Fatalf("icon = %q, want file: reference", ref)
	}
	if _, err := os.Stat(filepath.Join(icons, strings.TrimPrefix(ref, "file:"))); err != nil {
		t.Errorf("stored icon missing: %v", err)
	}

	if err := providerCmd([]string{"provider", "icon", "icos3-relay", "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if got := icoS3Icon(t, "icos3-relay"); got != "deepseek-color" {
		t.Fatalf("built-in icon = %q", got)
	}

	if err := providerCmd([]string{"provider", "icon", "icos3-relay", ""}); err != nil {
		t.Fatal(err)
	}
	if got := icoS3Icon(t, "icos3-relay"); got != "" {
		t.Fatalf("reset icon = %q, want empty", got)
	}
}

func TestIconS3RejectedKeepsOldIcon(t *testing.T) {
	icoS3Env(t)
	if err := provider.Save(provider.Provider{ID: "icos3-keep", Name: "Keep", Chat: "https://relay.example.com/v1", Key: "sk", Icon: "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	big := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, provider.MaxIcon)...)
	cases := map[string]string{
		"text":    icoS3Write(t, dir, "note.png", []byte("just some words")),
		"empty":   icoS3Write(t, dir, "empty.png", nil),
		"large":   icoS3Write(t, dir, "large.png", big),
		"missing": filepath.Join(dir, "nope.png"),
	}
	for name, path := range cases {
		if err := providerCmd([]string{"provider", "icon", "icos3-keep", path}); err == nil {
			t.Errorf("%s: icon accepted", name)
		}
		if got := icoS3Icon(t, "icos3-keep"); got != "deepseek-color" {
			t.Errorf("%s: icon changed to %q", name, got)
		}
	}
}

func TestIconS3SVGInTxtAccepted(t *testing.T) {
	icoS3Env(t)
	if err := provider.Save(provider.Provider{ID: "icos3-svg", Name: "Svg", Chat: "https://relay.example.com/v1", Key: "sk"}); err != nil {
		t.Fatal(err)
	}
	svg := icoS3Write(t, t.TempDir(), "logo.txt", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`))
	if err := providerCmd([]string{"provider", "icon", "icos3-svg", svg}); err != nil {
		t.Fatal(err)
	}
	if ref := icoS3Icon(t, "icos3-svg"); !strings.HasSuffix(ref, ".svg") {
		t.Fatalf("icon = %q, want *.svg reference", ref)
	}
}

func TestIconS3PresetAndAccountRefuse(t *testing.T) {
	icoS3Env(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	before := icoS3Icon(t, "deepseek")
	err = providerCmd([]string{"provider", "icon", "deepseek", "generic"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("preset icon error = %v, want one mentioning custom", err)
	}
	if got := icoS3Icon(t, "deepseek"); got != before {
		t.Errorf("preset icon changed to %q", got)
	}
	var p provider.Provider
	if err := applyPairs(&p, []string{"name=X", "url=https://relay.example.com/v1", "key=sk"}); err != nil {
		t.Fatal(err)
	}
	p.Preset = "deepseek"
	if err := applyPairs(&p, []string{"icon=generic"}); err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("applyPairs on preset: err = %v, want one mentioning custom", err)
	}
}
