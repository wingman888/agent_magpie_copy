package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// cliTestIsolate points magpie's config (providers.json and the icons folder)
// and the home folder at fresh temp directories for one test.
func cliTestIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

// captureStdout runs fn and returns what it printed to standard output.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	ferr := fn()
	os.Stdout = old
	w.Close()
	return <-done, ferr
}

func cliTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append([]byte("\x89PNG\r\n\x1a\n"), []byte("png-body")...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplyPairsHeaders(t *testing.T) {
	var p provider.Provider
	err := applyPairs(&p, []string{
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"Header.Authorization=user-token",
		"header.=dropped",
		"header.   =dropped too",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"X-ORG-id":      "padded",
		"X-Org-Id":      "acme",
		"Authorization": "user-token",
	}
	if len(p.Headers) != len(want) {
		t.Fatalf("Headers = %v, want %v", p.Headers, want)
	}
	for k, v := range want {
		if p.Headers[k] != v {
			t.Errorf("Headers[%q] = %q, want %q", k, p.Headers[k], v)
		}
	}
}

func TestShowProviderHeadersSorted(t *testing.T) {
	cliTestIsolate(t)
	if err := provider.Save(provider.Provider{
		ID: "my-relay", Name: "My Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "upper", "Authorization": "Bearer x"},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("my-relay")
	if err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{"header  Authorization: Bearer x", "header  X-ORG-id: upper", "header  X-Org-Id: acme"}
	last := -1
	for _, l := range lines {
		i := strings.Index(out, l)
		if i < 0 {
			t.Fatalf("output lacks %q:\n%s", l, out)
		}
		if i < last {
			t.Fatalf("%q out of order:\n%s", l, out)
		}
		last = i
	}
}

func TestIconArg(t *testing.T) {
	cliTestIsolate(t)
	dir := t.TempDir()
	if got, err := iconArg(""); err != nil || got != "" {
		t.Errorf(`iconArg("") = %q, %v; want "", nil`, got, err)
	}
	if got, err := iconArg("deepseek-color"); err != nil || got != "deepseek-color" {
		t.Errorf("iconArg(builtin) = %q, %v", got, err)
	}
	if _, err := iconArg(filepath.Join(dir, "missing.png")); err == nil {
		t.Error("missing image path should be an error")
	}
	png := cliTestPNG(t, dir, "logo.png")
	got, err := iconArg(png)
	if err != nil || !strings.HasPrefix(got, "file:") || !strings.HasSuffix(got, ".png") {
		t.Errorf("iconArg(png) = %q, %v; want file:…png", got, err)
	}
	txt := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txt, []byte("just text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := iconArg(txt); err == nil {
		t.Error("text file should be rejected")
	}
}

func TestProviderIconCommand(t *testing.T) {
	cliTestIsolate(t)
	dir := t.TempDir()
	if err := provider.Save(provider.Provider{ID: "my-relay", Name: "My Relay", Chat: "https://relay.example.com/v1", Key: "sk-test"}); err != nil {
		t.Fatal(err)
	}
	icon := func() string {
		p, err := provider.Find("my-relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.Icon
	}
	set := func(v string) error {
		_, err := captureStdout(t, func() error {
			return providerCmd([]string{"provider", "icon", "my-relay", v})
		})
		return err
	}

	png := cliTestPNG(t, dir, "logo.png")
	if err := set(png); err != nil {
		t.Fatal(err)
	}
	ref := icon()
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("icon = %q, want file:…png", ref)
	}
	if err := set(png); err != nil || icon() != ref {
		t.Fatalf("same image again gave %q (err %v), want %q", icon(), err, ref)
	}

	if err := set("deepseek-color"); err != nil || icon() != "deepseek-color" {
		t.Fatalf("builtin: icon = %q, err %v", icon(), err)
	}

	txt := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txt, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := set(txt); err == nil || icon() != "deepseek-color" {
		t.Fatalf("text file: err %v, icon %q; want error and icon unchanged", err, icon())
	}

	if err := set(""); err != nil || icon() != "" {
		t.Fatalf("reset: icon = %q, err %v; want empty", icon(), err)
	}
}

func TestProviderIconRejectsPresetAndAccount(t *testing.T) {
	cliTestIsolate(t)
	if err := provider.Save(provider.Provider{ID: "mine", Name: "Mine", Preset: "deepseek", Chat: "https://api.deepseek.com/v1", Key: "sk-test"}); err != nil {
		t.Fatal(err)
	}
	before, err := provider.Find("mine")
	if err != nil {
		t.Fatal(err)
	}
	_, err = captureStdout(t, func() error {
		return providerCmd([]string{"provider", "icon", "mine", "generic"})
	})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("preset icon: err = %v, want a 'custom' error", err)
	}
	after, _ := provider.Find("mine")
	if after.Icon != before.Icon {
		t.Fatalf("preset icon changed from %q to %q", before.Icon, after.Icon)
	}
}

func TestAddPresetRejectsIcon(t *testing.T) {
	cliTestIsolate(t)
	err := addProvider([]string{"deepseek", "sk-test", "icon=deepseek-color"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("add preset with icon: err = %v, want a 'custom' error", err)
	}
	if _, err := provider.Find("deepseek"); err == nil {
		t.Fatal("preset was saved despite the rejected icon")
	}
}
