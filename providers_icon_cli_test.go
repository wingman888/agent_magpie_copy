package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// setupIconConfig points magpie's config and cache at fresh temp directories.
func setupIconConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// pngBytes is a small blob that the icon sniffer recognizes as PNG.
func pngBytes(extra int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, extra)...)
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func iconsDirForTest() string {
	return filepath.Join(filepath.Dir(provider.Path()), "icons")
}

func TestAddIconFileIsStored(t *testing.T) {
	setupIconConfig(t)
	src := writeTemp(t, "logo.png", pngBytes(64))
	var p provider.Provider
	if err := applyPairs(&p, []string{"url=https://relay.example.com/v1", "icon=" + src}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Fatalf("Icon = %q, want file:…png", p.Icon)
	}
	stored, err := os.ReadFile(provider.IconFile(strings.TrimPrefix(p.Icon, "file:")))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, pngBytes(64)) {
		t.Error("stored icon differs from the source file")
	}
}

func TestAddIconSameContentSameRef(t *testing.T) {
	setupIconConfig(t)
	a := writeTemp(t, "a.png", pngBytes(32))
	b := writeTemp(t, "copy.png", pngBytes(32))
	var p1, p2 provider.Provider
	if err := applyPairs(&p1, []string{"icon=" + a}); err != nil {
		t.Fatal(err)
	}
	if err := applyPairs(&p2, []string{"icon=" + b}); err != nil {
		t.Fatal(err)
	}
	if p1.Icon == "" || p1.Icon != p2.Icon {
		t.Fatalf("refs differ: %q vs %q", p1.Icon, p2.Icon)
	}
	entries, err := os.ReadDir(iconsDirForTest())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("icons dir has %d files, want 1", len(entries))
	}
}

func TestAddIconBuiltinNameKept(t *testing.T) {
	setupIconConfig(t)
	var p provider.Provider
	if err := applyPairs(&p, []string{"icon=  deepseek-color  "}); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "deepseek-color" {
		t.Errorf("Icon = %q, want deepseek-color", p.Icon)
	}
}

func TestAddIconRejectedFilesKeepOldIcon(t *testing.T) {
	setupIconConfig(t)
	cases := map[string][]byte{
		"notes.txt": []byte("just some text, not a picture"),
		"empty.png": {},
		"huge.png":  pngBytes(provider.MaxIcon),
	}
	for name, data := range cases {
		path := writeTemp(t, name, data)
		p := provider.Provider{Icon: "generic"}
		if err := applyPairs(&p, []string{"icon=" + path}); err == nil {
			t.Errorf("%s accepted as icon", name)
		}
		if p.Icon != "generic" {
			t.Errorf("%s changed Icon to %q", name, p.Icon)
		}
	}
}

func TestAddIconMissingFileRejected(t *testing.T) {
	setupIconConfig(t)
	var p provider.Provider
	if err := applyPairs(&p, []string{"icon=./no-such-logo.png"}); err == nil {
		t.Error("missing icon file accepted")
	}
}

func TestAddIconRefusedForPreset(t *testing.T) {
	setupIconConfig(t)
	p := provider.Provider{Name: "DeepSeek", Preset: "deepseek"}
	err := applyPairs(&p, []string{"icon=generic"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Errorf("preset icon error = %v, want one mentioning custom", err)
	}
}

func TestProviderIconCommand(t *testing.T) {
	setupIconConfig(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	src := writeTemp(t, "logo.png", pngBytes(16))
	captureStdout(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", src}); err != nil {
			t.Fatal(err)
		}
	})
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") {
		t.Fatalf("Icon = %q, want file: reference", p.Icon)
	}

	captureStdout(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", "deepseek-color"}); err != nil {
			t.Fatal(err)
		}
	})
	if p, _ = provider.Find("relay"); p.Icon != "deepseek-color" {
		t.Fatalf("Icon = %q, want deepseek-color", p.Icon)
	}

	captureStdout(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", ""}); err != nil {
			t.Fatal(err)
		}
	})
	if p, _ = provider.Find("relay"); p.Icon != "" {
		t.Fatalf("Icon = %q after reset, want empty", p.Icon)
	}
}

func TestProviderIconRejectionKeepsIcon(t *testing.T) {
	setupIconConfig(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-1", Icon: "generic"}); err != nil {
		t.Fatal(err)
	}
	bad := writeTemp(t, "notes.txt", []byte("plain text"))
	captureStdout(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", bad}); err == nil {
			t.Error("text file accepted as icon")
		}
	})
	captureStdout(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", "./missing.png"}); err == nil {
			t.Error("missing file accepted as icon")
		}
	})
	if p, _ := provider.Find("relay"); p.Icon != "generic" {
		t.Errorf("Icon = %q, want generic kept", p.Icon)
	}
}

func TestProviderIconRefusedForPreset(t *testing.T) {
	setupIconConfig(t)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "sk-1"}); err != nil {
		t.Fatal(err)
	}
	before, _ := provider.Find("deepseek")
	captureStdout(t, func() {
		err := providerCmd([]string{"provider", "icon", "deepseek", "generic"})
		if err == nil || !strings.Contains(err.Error(), "custom") {
			t.Errorf("preset icon error = %v, want one mentioning custom", err)
		}
	})
	if after, _ := provider.Find("deepseek"); after.Icon != before.Icon {
		t.Errorf("preset icon changed from %q to %q", before.Icon, after.Icon)
	}
}

func TestUsageListsIconAndHeaders(t *testing.T) {
	// Each item is checked on its own against the provider usage text, which
	// is where `magpie provider` help is printed.
	for _, want := range []string{"header.X-Foo", "icon", "provider icon"} {
		if !strings.Contains(providerUsage, want) {
			t.Errorf("providerUsage lacks %q", want)
		}
	}
}

func TestAddIconDirectoryNameIsBuiltin(t *testing.T) {
	setupIconConfig(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "generic"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	var p provider.Provider
	if err := applyPairs(&p, []string{"icon=generic"}); err != nil {
		t.Fatalf("directory-named icon rejected: %v", err)
	}
	if p.Icon != "generic" {
		t.Errorf("Icon = %q, want generic", p.Icon)
	}
}

func TestProviderIconRefusedForSignedInAgent(t *testing.T) {
	setupIconConfig(t)
	// A codex sign-in: auth.json under HOME makes codex a signed-in account.
	codexDir := filepath.Join(os.Getenv("HOME"), ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatal(err)
	}
	auth := `{"auth_mode":"chatgpt","tokens":{"access_token":"tok","refresh_token":"r"}}`
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil || p.Account == nil {
		t.Fatalf("codex is not a signed-in account: %v %+v", err, p)
	}
	before := p.Icon
	err = providerCmd([]string{"provider", "icon", "codex", "generic"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Errorf("agent icon error = %v, want one mentioning custom", err)
	}
	if after, _ := provider.Find("codex"); after.Icon != before {
		t.Errorf("agent icon changed from %q to %q", before, after.Icon)
	}
}
