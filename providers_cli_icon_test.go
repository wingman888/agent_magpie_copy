package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// iconCLIEnv points every config and cache location at a fresh temp dir.
func iconCLIEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
}

// iconCLIPNG is a small byte slice that starts with the PNG signature.
func iconCLIPNG(extra string) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), []byte(extra)...)
}

// iconCLIWrite writes data to a file in a fresh temp dir and returns its path.
func iconCLIWrite(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// iconCLIIconDir is the icons directory next to providers.json.
func iconCLIIconDir() string {
	return filepath.Join(filepath.Dir(provider.Path()), "icons")
}

// iconCLICustom saves a custom provider with the given id and returns it.
func iconCLICustom(t *testing.T, id string) provider.Provider {
	t.Helper()
	p := provider.Provider{ID: id, Name: "Icon " + id, Chat: "https://relay.example.com/v1", Key: "sk-test"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return *got
}

// iconCLIIconOf reads back a provider's stored icon field.
func iconCLIIconOf(t *testing.T, id string) string {
	t.Helper()
	got, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return got.Icon
}

func TestIconCLIAddStoresFileReference(t *testing.T) {
	iconCLIEnv(t)
	data := iconCLIPNG("add-file")
	path := iconCLIWrite(t, "logo.png", data)

	p := provider.Provider{Name: "Acme", Chat: "https://relay.example.com/v1", Key: "sk-test"}
	if err := applyPairs(&p, []string{"icon=" + path}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Fatalf("icon = %q, want a file: reference to a .png", p.Icon)
	}
	name := strings.TrimPrefix(p.Icon, "file:")
	stored, err := os.ReadFile(provider.IconFile(name))
	if err != nil {
		t.Fatalf("stored icon missing: %v", err)
	}
	if !bytes.Equal(stored, data) {
		t.Fatal("stored icon content differs from the source file")
	}
}

func TestIconCLIAddBuiltinNameAndTrim(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"icon=  deepseek-color  "}); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "deepseek-color" {
		t.Fatalf("icon = %q, want deepseek-color", p.Icon)
	}
}

func TestIconCLIAddRejectsTextKeepsIcon(t *testing.T) {
	iconCLIEnv(t)
	path := iconCLIWrite(t, "notes.txt", []byte("just some text, not an image"))
	p := provider.Provider{Name: "Acme", Chat: "https://relay.example.com/v1", Key: "sk-test", Icon: "generic"}
	err := applyPairs(&p, []string{"icon=" + path})
	if err == nil {
		t.Fatal("text file accepted as icon")
	}
	if p.Icon != "generic" {
		t.Fatalf("icon changed to %q on rejection", p.Icon)
	}
}

func TestIconCLIAddPresetRefusesIcon(t *testing.T) {
	iconCLIEnv(t)
	err := addProvider([]string{"deepseek", "sk-test", "icon=generic"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("err = %v, want a 'custom' refusal", err)
	}
	if _, err := provider.Find("deepseek"); err == nil {
		t.Fatal("preset was saved despite the refused icon")
	}
}

func TestIconCLISetFileThenDedupe(t *testing.T) {
	iconCLIEnv(t)
	iconCLICustom(t, "acme")
	path := iconCLIWrite(t, "logo.png", iconCLIPNG("dedupe"))

	if err := providerCmd([]string{"magpie", "icon", "acme", path}); err != nil {
		t.Fatal(err)
	}
	first := iconCLIIconOf(t, "acme")
	if !strings.HasPrefix(first, "file:") {
		t.Fatalf("icon = %q, want a file: reference", first)
	}
	if err := providerCmd([]string{"magpie", "icon", "acme", path}); err != nil {
		t.Fatal(err)
	}
	if again := iconCLIIconOf(t, "acme"); again != first {
		t.Fatalf("second set gave %q, want %q", again, first)
	}
	entries, err := os.ReadDir(iconCLIIconDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("icons dir has %d files, want 1", len(entries))
	}
}

func TestIconCLISetBuiltinThenReset(t *testing.T) {
	iconCLIEnv(t)
	iconCLICustom(t, "acme")

	if err := providerCmd([]string{"magpie", "icon", "acme", "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if got := iconCLIIconOf(t, "acme"); got != "deepseek-color" {
		t.Fatalf("icon = %q, want deepseek-color", got)
	}
	if err := providerCmd([]string{"magpie", "icon", "acme", ""}); err != nil {
		t.Fatal(err)
	}
	if got := iconCLIIconOf(t, "acme"); got != "" {
		t.Fatalf("icon = %q after reset, want empty", got)
	}
}

func TestIconCLIRejectedImageKeepsIcon(t *testing.T) {
	iconCLIEnv(t)
	iconCLICustom(t, "acme")
	if err := providerCmd([]string{"magpie", "icon", "acme", "deepseek-color"}); err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"text":  []byte("plain text, definitely not an image"),
		"empty": {},
		"large": append(iconCLIPNG("big"), make([]byte, provider.MaxIcon)...),
	}
	for name, data := range cases {
		path := iconCLIWrite(t, name+".png", data)
		if err := providerCmd([]string{"magpie", "icon", "acme", path}); err == nil {
			t.Errorf("%s file accepted as icon", name)
		}
		if got := iconCLIIconOf(t, "acme"); got != "deepseek-color" {
			t.Errorf("%s file changed icon to %q", name, got)
		}
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist.png")
	if err := providerCmd([]string{"magpie", "icon", "acme", missing}); err == nil {
		t.Error("missing path accepted")
	}
	if got := iconCLIIconOf(t, "acme"); got != "deepseek-color" {
		t.Errorf("missing path changed icon to %q", got)
	}
}

func TestIconCLIPresetRefusesIcon(t *testing.T) {
	iconCLIEnv(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	before := iconCLIIconOf(t, "deepseek")

	err = providerCmd([]string{"magpie", "icon", "deepseek", "generic"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("err = %v, want a 'custom' refusal", err)
	}
	if got := iconCLIIconOf(t, "deepseek"); got != before {
		t.Fatalf("preset icon changed from %q to %q", before, got)
	}
}
