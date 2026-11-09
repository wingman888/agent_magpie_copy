package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func hdrtCLIIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestHdrtAddParsesHeaderPairs(t *testing.T) {
	var p provider.Provider
	if err := applyPairs(&p, []string{"header.X-Org-Id=acme", "HEADER.x-trace=t1"}); err != nil {
		t.Fatal(err)
	}
	if p.Headers["X-Org-Id"] != "acme" || p.Headers["x-trace"] != "t1" || len(p.Headers) != 2 {
		t.Errorf("headers = %v", p.Headers)
	}
	if err := applyPairs(&p, []string{"header.=oops"}); err == nil {
		t.Error("header pair with no name accepted")
	}
}

func TestHdrtIconRejectedForPreset(t *testing.T) {
	pr, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	err = applyPairs(&pr, []string{"icon=openai"})
	if err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置图标") {
		t.Errorf("preset icon error = %v", err)
	}
}

func TestHdrtIconBuiltinName(t *testing.T) {
	p := provider.Provider{Name: "Hdrt Own"}
	if err := applyPairs(&p, []string{"icon=openai"}); err != nil {
		t.Fatal(err)
	}
	if p.Icon != "openai" {
		t.Errorf("icon = %q", p.Icon)
	}
	if err := applyPairs(&p, []string{"icon="}); err != nil || p.Icon != "" {
		t.Errorf("empty icon: %q, %v", p.Icon, err)
	}
}

func TestHdrtIconFileIsStored(t *testing.T) {
	hdrtCLIIsolate(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "logo.png")
	data := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{5}, 32)...)
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	p := provider.Provider{Name: "Hdrt File"}
	if err := applyPairs(&p, []string{"icon=" + src}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Icon, "file:") {
		t.Fatalf("icon = %q, want file:<name>", p.Icon)
	}
	if _, err := os.Stat(provider.IconFile(strings.TrimPrefix(p.Icon, "file:"))); err != nil {
		t.Errorf("stored icon missing: %v", err)
	}
}

func TestHdrtIconMissingFileRejected(t *testing.T) {
	hdrtCLIIsolate(t)
	if _, err := iconArg(filepath.Join(t.TempDir(), "nope.png")); err == nil {
		t.Error("missing picture file accepted")
	}
	if _, err := iconArg("notes.txt"); err == nil {
		t.Error("a file name with no picture on disk was taken as a built-in name")
	}
}
