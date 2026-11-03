package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveProxyValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	ok := []string{"", "direct", "127.0.0.1:7890", "http://127.0.0.1:7890",
		"https://proxy.example.com:443", "socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080",
		"  socks5://127.0.0.1:1080  "}
	for _, p := range ok {
		if err := Save(Settings{Proxy: p}); err != nil {
			t.Errorf("Save(%q) rejected: %v", p, err)
		}
	}

	bad := []string{"ftp://x", "http://", "http:///path", "socks5://:1080", "://", "not a proxy", "http://[::1"}
	for _, p := range bad {
		err := Save(Settings{Proxy: p})
		if err == nil {
			t.Errorf("Save(%q) accepted", p)
			continue
		}
		if !strings.Contains(err.Error(), ProxyExample) {
			t.Errorf("Save(%q) error lacks example: %v", p, err)
		}
	}
}

func TestSaveProxyTrimAndPersist(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "  socks5://127.0.0.1:1080 \n"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("proxy not trimmed: %q", got)
	}
	if err := Save(Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "direct" {
		t.Fatalf("direct not kept: %q", got)
	}
}

func TestSaveProxyFailureKeepsFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Theme: "dark", Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{Theme: "light", Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp accepted")
	}
	after, err := os.ReadFile(filepath.Clean(Path()))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("file changed after failed save:\n%s\n%s", before, after)
	}
	if s := Load(); s.Theme != "dark" || s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("settings changed: %+v", s)
	}
}
