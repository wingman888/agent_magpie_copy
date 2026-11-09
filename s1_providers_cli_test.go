package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// s1CLIEnv keeps the CLI tests out of the real config, cache and home.
func s1CLIEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("HOME", filepath.Join(dir, "home"))
	return dir
}

func TestS1ApplyPairsHeadersAndIcon(t *testing.T) {
	dir := s1CLIEnv(t)
	p := provider.Provider{Name: "Acme"}
	if err := applyPairs(&p, []string{"url=https://a.example/v1", "header.X-Org-Id=acme", "HEADER.x-trace=1"}); err != nil {
		t.Fatal(err)
	}
	if len(p.Headers) != 2 || p.Headers["X-Org-Id"] != "acme" || p.Headers["x-trace"] != "1" {
		t.Fatalf("headers: %#v", p.Headers)
	}
	if err := applyPairs(&p, []string{"header.=x"}); err == nil {
		t.Error("a header with no name was accepted")
	}

	pic := filepath.Join(dir, "logo.png")
	if err := os.WriteFile(pic, []byte("\x89PNG\r\n\x1a\n-s1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyPairs(&p, []string{"icon=" + pic}); err != nil {
		t.Fatal(err)
	}
	name, ok := strings.CutPrefix(p.Icon, "file:")
	if !ok || provider.IconFile(name) == "" {
		t.Fatalf("icon value %q does not resolve", p.Icon)
	}

	txt := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyPairs(&p, []string{"icon=" + txt}); err == nil {
		t.Error("a text file was accepted as an icon")
	}

	q := provider.Provider{Name: "Q"}
	if err := applyPairs(&q, []string{"icon=openai"}); err != nil || q.Icon != "openai" {
		t.Fatalf("built-in icon: %q, %v", q.Icon, err)
	}

	pre, err := provider.FromPreset("deepseek")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyPairs(&pre, []string{"icon=" + pic}); err == nil || !strings.Contains(err.Error(), "仅自定义供应商可设置图标") {
		t.Fatalf("preset icon: %v", err)
	}
}

func TestS1IconArgEmptyResetsAndMissingFails(t *testing.T) {
	s1CLIEnv(t)
	if got, err := iconArg(""); err != nil || got != "" {
		t.Fatalf("empty: %q, %v", got, err)
	}
	if _, err := iconArg("no-such-icon-or-file"); err == nil {
		t.Fatal("an unknown name was accepted")
	}
}
