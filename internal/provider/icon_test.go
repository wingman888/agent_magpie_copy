package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreIcon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	ref, err := StoreIcon(png)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file:") || !strings.HasSuffix(ref, ".png") {
		t.Fatalf("ref = %q", ref)
	}
	if again, _ := StoreIcon(png); again != ref {
		t.Errorf("same picture stored twice: %q, %q", ref, again)
	}
	p := IconFile(strings.TrimPrefix(ref, "file:"))
	if b, err := os.ReadFile(p); err != nil || string(b) != string(png) {
		t.Fatalf("IconFile(%q) = %q: %v", ref, p, err)
	}

	if ref, err := StoreIcon([]byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`)); err != nil || !strings.HasSuffix(ref, ".svg") {
		t.Errorf("svg: %q, %v", ref, err)
	}
	if _, err := StoreIcon([]byte("hello")); err == nil {
		t.Error("text taken as a picture")
	}
	if _, err := StoreIcon(make([]byte, MaxIcon+1)); err == nil {
		t.Error("oversized picture taken")
	}
	for _, bad := range []string{"../providers.json", "x.png", "0123456789abcdef.exe", ""} {
		if IconFile(bad) != "" {
			t.Errorf("IconFile(%q) resolved", bad)
		}
	}
}
