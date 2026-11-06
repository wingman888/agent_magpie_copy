package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func cliHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestAddHeaderFieldsAndIconFile(t *testing.T) {
	cliHome(t)
	img := filepath.Join(t.TempDir(), "logo.png")
	if err := os.WriteFile(img, append([]byte("\x89PNG\r\n\x1a\n"), []byte("logo")...), 0o644); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{Name: "Acme"}
	if err := applyPairs(&p, []string{"header.X-Org-Id=acme", "Header.x-Other=1", "icon=" + img}); err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" || p.Headers["x-Other"] != "1" || len(p.Headers) != 2 {
		t.Errorf("headers parsed wrongly: %#v", p.Headers)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Errorf("icon file not stored as a reference: %q", p.Icon)
	}
	if err := applyPairs(&p, []string{"header.=x"}); err == nil {
		t.Error("header without a name accepted")
	}
}

func TestPresetRefusesIconField(t *testing.T) {
	cliHome(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPairs(&pr, []string{"icon=openai"}); err == nil || !strings.Contains(err.Error(), "only custom providers") {
		t.Errorf("preset icon not refused clearly: %v", err)
	}
}

func TestIconCommandRefusesPreset(t *testing.T) {
	cliHome(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "k"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	err = providerCmd([]string{"provider", "icon", "deepseek", ""})
	if err == nil || !strings.Contains(err.Error(), "only custom providers") {
		t.Errorf("preset icon command not refused clearly: %v", err)
	}
}

func TestIconValueRejectsMissingOrNonImage(t *testing.T) {
	cliHome(t)
	if _, err := iconValue(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Error("missing image file accepted")
	}
	txt := filepath.Join(t.TempDir(), "note.txt")
	os.WriteFile(txt, []byte("just some text"), 0o644)
	if _, err := iconValue(txt); err == nil {
		t.Error("plain text file accepted as an icon")
	}
	if v, err := iconValue(""); err != nil || v != "" {
		t.Errorf("empty icon should restore the default: %q %v", v, err)
	}
	if v, err := iconValue("openai"); err != nil || v != "openai" {
		t.Errorf("built-in name changed: %q %v", v, err)
	}
}
