package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// f04Isolate points the config and home directories at a fresh temp dir so the
// tests never touch the user's real providers.
func f04Isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// f04Capture runs fn with stdout redirected and returns what it printed.
func f04Capture(t *testing.T, fn func()) string {
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
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func f04WriteFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestF04ApplyPairsHeaders(t *testing.T) {
	f04Isolate(t)
	p := provider.Provider{Name: "Relay"}
	err := applyPairs(&p, []string{
		"HEADER.X-ORG-id =  padded  ",
		"header.X-Org-Id=acme",
		"header.Authorization=Bearer other",
		"header.=dropped",
		"header.   =dropped too",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"X-ORG-id":      "padded",
		"X-Org-Id":      "acme",
		"Authorization": "Bearer other",
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

func TestF04ApplyPairsIconRejectedOnPreset(t *testing.T) {
	f04Isolate(t)
	p, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	err = applyPairs(&p, []string{"icon=deepseek-color"})
	if err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("err = %v, want a message mentioning custom", err)
	}
}

func TestF04IconValue(t *testing.T) {
	f04Isolate(t)

	if v, err := iconValue(""); err != nil || v != "" {
		t.Errorf(`iconValue("") = %q, %v; want "", nil`, v, err)
	}
	if v, err := iconValue("deepseek-color"); err != nil || v != "deepseek-color" {
		t.Errorf("built-in name = %q, %v", v, err)
	}
	if _, err := iconValue("./missing.png"); err == nil {
		t.Error("missing file path accepted as a built-in name")
	}
	if _, err := iconValue(f04WriteFile(t, "note.txt", []byte("just some text"))); err == nil {
		t.Error("text file accepted as icon")
	}
	if _, err := iconValue(f04WriteFile(t, "empty.png", nil)); err == nil {
		t.Error("empty file accepted as icon")
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	ref, err := iconValue(f04WriteFile(t, "logo.png", png))
	if err != nil {
		t.Fatalf("png rejected: %v", err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Errorf("png ref = %q, want file:<hash>.png", ref)
	}
	again, err := iconValue(f04WriteFile(t, "same.png", png))
	if err != nil || again != ref {
		t.Errorf("same image gave %q, %v; want %q", again, err, ref)
	}
}

func TestF04ShowProviderHeaders(t *testing.T) {
	f04Isolate(t)
	p := provider.Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Headers: map[string]string{"X-Org-Id": "acme", "X-ORG-id": "x", "Authorization": "Bearer y"},
	}
	out := f04Capture(t, func() {
		if err := showProvider(p); err != nil {
			t.Fatal(err)
		}
	})
	lines := []string{"header  Authorization: Bearer y", "header  X-ORG-id: x", "header  X-Org-Id: acme"}
	last := -1
	for _, l := range lines {
		i := strings.Index(out, l)
		if i < 0 {
			t.Fatalf("output missing %q:\n%s", l, out)
		}
		if i < last {
			t.Errorf("%q printed out of order:\n%s", l, out)
		}
		last = i
	}
}

func TestF04ProviderIconCommand(t *testing.T) {
	f04Isolate(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test"}); err != nil {
		t.Fatal(err)
	}
	f04Capture(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", "deepseek-color"}); err != nil {
			t.Fatal(err)
		}
	})
	if p, _ := provider.Find("relay"); p.Icon != "deepseek-color" {
		t.Fatalf("icon = %q, want deepseek-color", p.Icon)
	}

	// A rejected image leaves the previous icon in place.
	bad := f04WriteFile(t, "note.txt", []byte("plain text"))
	f04Capture(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", bad}); err == nil {
			t.Fatal("text file accepted")
		}
	})
	if p, _ := provider.Find("relay"); p.Icon != "deepseek-color" {
		t.Fatalf("icon changed to %q after rejected file", p.Icon)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	f04Capture(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", f04WriteFile(t, "logo.png", png)}); err != nil {
			t.Fatal(err)
		}
	})
	if p, _ := provider.Find("relay"); !strings.HasPrefix(p.Icon, "file:") {
		t.Fatalf("icon = %q, want a file: reference", p.Icon)
	}

	f04Capture(t, func() {
		if err := providerCmd([]string{"provider", "icon", "relay", ""}); err != nil {
			t.Fatal(err)
		}
	})
	if p, _ := provider.Find("relay"); p.Icon != "" {
		t.Fatalf("icon = %q after reset, want empty", p.Icon)
	}
}

func TestF04ProviderIconCommandRejectsPreset(t *testing.T) {
	f04Isolate(t)
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	pr.Key = "sk-test"
	if err := provider.Save(pr); err != nil {
		t.Fatal(err)
	}
	f04Capture(t, func() {
		err := providerCmd([]string{"provider", "icon", "deepseek", "openai"})
		if err == nil || !strings.Contains(err.Error(), "custom") {
			t.Fatalf("err = %v, want a message mentioning custom", err)
		}
	})
	if p, _ := provider.Find("deepseek"); p.Icon != "deepseek-color" {
		t.Fatalf("preset icon changed to %q", p.Icon)
	}
}
