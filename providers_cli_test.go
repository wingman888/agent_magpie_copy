package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// isolateCLI points the config and cache at a fresh temp dir so tests never
// touch the real providers.json or icon directory.
func isolateCLI(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// captureStdout runs fn and returns what it printed to os.Stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()
	fn()
	w.Close()
	os.Stdout = old
	return string(<-done)
}

func writeFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func pngBytes(extra int) []byte {
	return append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, extra)...)
}

func saveCustom(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{
		ID: "relay", Name: "My Relay", Key: "sk-test", Chat: "https://relay.example.com/v1",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"url=https://relay.example.com/v1",
		"header.X-Org-Id=acme",
		"HEADER.X-ORG-id =  padded  ",
		"header.=dropped",
		"header.   =dropped too",
		"header.Authorization=Bearer a=b",
		"header.X-Org-Id=later",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"X-Org-Id":      "later",
		"X-ORG-id":      "padded",
		"Authorization": "Bearer a=b",
	}
	if len(p.Headers) != len(want) {
		t.Fatalf("headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("header %q = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestApplyPairsKeepsTrimmedValues(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"  name  =  My Relay  ", "key= sk-x "}); err != nil {
		t.Fatal(err)
	}
	if p.Name != "My Relay" || p.Key != "sk-x" {
		t.Errorf("name=%q key=%q, want trimmed values", p.Name, p.Key)
	}
}

func TestShowProviderHeadersSorted(t *testing.T) {
	isolateCLI(t)
	if err := provider.Save(provider.Provider{
		ID: "relay", Name: "My Relay", Key: "sk-test", Chat: "https://relay.example.com/v1",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "upper", "Authorization": "Token x"},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := showProvider(*p); err != nil {
			t.Fatal(err)
		}
	})
	lines := []string{"header  Authorization: Token x", "header  X-ORG-id: upper", "header  X-Org-Id: acme"}
	last := -1
	for _, l := range lines {
		i := strings.Index(out, l)
		if i < 0 {
			t.Fatalf("output missing %q:\n%s", l, out)
		}
		if i < last {
			t.Errorf("%q is out of order:\n%s", l, out)
		}
		last = i
	}
}

func TestAddIconFileStoresOnce(t *testing.T) {
	isolateCLI(t)
	saveCustom(t)
	img := writeFile(t, "logo.png", pngBytes(64))

	if err := providerCmd([]string{"provider", "icon", "relay", img}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") || !strings.HasSuffix(p.Icon, ".png") {
		t.Fatalf("icon = %q, want file:<name>.png", p.Icon)
	}
	first := p.Icon
	if provider.IconFile(strings.TrimPrefix(first, "file:")) == "" {
		t.Fatalf("IconFile rejected %q", first)
	}

	if err := providerCmd([]string{"provider", "icon", "relay", img}); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("relay")
	if p.Icon != first {
		t.Errorf("second set gave %q, want %q", p.Icon, first)
	}
	entries, err := os.ReadDir(filepath.Join(filepath.Dir(provider.Path()), "icons"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("icons dir has %d files, want 1", len(entries))
	}
}

func TestIconBuiltinAndReset(t *testing.T) {
	isolateCLI(t)
	saveCustom(t)
	if err := providerCmd([]string{"provider", "icon", "relay", "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("relay"); p.Icon != "deepseek-color" {
		t.Fatalf("icon = %q, want deepseek-color", p.Icon)
	}
	if err := providerCmd([]string{"provider", "icon", "relay", ""}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("relay"); p.Icon != "" {
		t.Fatalf("icon = %q after reset, want empty", p.Icon)
	}
}

func TestIconRejectsKeepsOldValue(t *testing.T) {
	isolateCLI(t)
	saveCustom(t)
	if err := providerCmd([]string{"provider", "icon", "relay", "deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"text":    writeFile(t, "note.txt", []byte("just some text, not an image")),
		"empty":   writeFile(t, "empty.png", nil),
		"too big": writeFile(t, "big.png", pngBytes(provider.MaxIcon)),
		"missing": filepath.Join(t.TempDir(), "missing.png"),
	}
	for name, path := range cases {
		if err := providerCmd([]string{"provider", "icon", "relay", path}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
		if p, _ := provider.Find("relay"); p.Icon != "deepseek-color" {
			t.Errorf("%s: icon changed to %q", name, p.Icon)
		}
	}
}

func TestIconAcceptsSVGWithTxtExtension(t *testing.T) {
	isolateCLI(t)
	saveCustom(t)
	svg := writeFile(t, "logo.txt", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`))
	if err := providerCmd([]string{"provider", "icon", "relay", svg}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("relay")
	if !strings.HasSuffix(p.Icon, ".svg") {
		t.Errorf("icon = %q, want an .svg reference", p.Icon)
	}
}

func TestIconRefusedForPreset(t *testing.T) {
	isolateCLI(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	before, _ := provider.Find("deepseek")
	err = providerCmd([]string{"provider", "icon", "deepseek", "generic"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("err = %v, want an error mentioning custom", err)
	}
	after, _ := provider.Find("deepseek")
	if after.Icon != before.Icon {
		t.Errorf("preset icon changed from %q to %q", before.Icon, after.Icon)
	}
}

func TestAddResolvesIconAndRejectsBadFile(t *testing.T) {
	isolateCLI(t)
	p := provider.Provider{Name: "My Relay"}
	if err := applyPairs(&p, []string{"icon=deepseek-color"}); err != nil {
		t.Fatal(err)
	}
	icon, err := resolveIcon(p.Icon)
	if err != nil || icon != "deepseek-color" {
		t.Fatalf("resolveIcon = %q, %v; want deepseek-color", icon, err)
	}

	bad := writeFile(t, "note.txt", []byte("plain text"))
	p = provider.Provider{Name: "My Relay"}
	if err := applyPairs(&p, []string{"icon=" + bad}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveIcon(p.Icon); err == nil {
		t.Fatal("text file accepted as icon")
	}
}

func TestHelpMentionsHeadersAndIcon(t *testing.T) {
	for _, want := range []string{"header.X-Foo", "icon", "provider icon"} {
		if !strings.Contains(providerUsage, want) {
			t.Errorf("providerUsage does not mention %q", want)
		}
	}
}
