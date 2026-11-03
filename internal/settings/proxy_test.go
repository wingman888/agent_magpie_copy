package settings

import (
	"os"
	"strings"
	"testing"
)

func TestSaveProxyAccepted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{"127.0.0.1:7890", "direct", "", "http://127.0.0.1:7890", "https://h:443", "socks5://127.0.0.1:1080", "socks5h://proxy.local", "socks5h://127.0.0.1:1080", "[::1]:7890", "http://user:pass@proxy.local:3128", "http://h/", "http://h/some/path"} {
		if err := Save(Settings{Proxy: v}); err != nil {
			t.Fatalf("Save(%q) failed: %v", v, err)
		}
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "127.0.0.1:7890" {
		t.Fatalf("Proxy after save = %q", got)
	}
	if err := Save(Settings{Proxy: "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "direct" {
		t.Fatalf("direct not normalized: %q", got)
	}
	if err := Save(Settings{}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "" {
		t.Fatalf("empty proxy not kept empty: %q", got)
	}
}

func TestSaveProxyTrimmed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "  socks5://127.0.0.1:1080 \n"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("not trimmed: %q", got)
	}
}

func TestSaveProxyRejected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, v := range []string{"ftp://x", "http://", "socks5://:1080", "http://h:99999", "http://h:abc", "::::", "http://h:0", "http://[::1]:99999", "http://[::1"} {
		err := Save(Settings{Proxy: v})
		if err == nil {
			t.Fatalf("Save(%q) accepted", v)
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Fatalf("Save(%q) error lacks example: %v", v, err)
		}
		if ValidateProxy(v) == nil {
			t.Fatalf("ValidateProxy(%q) accepted", v)
		}
	}
	if err := ValidateProxy("127.0.0.1:7890"); err != nil {
		t.Fatal(err)
	}
}

func TestSaveProxyRejectedKeepsFile(t *testing.T) {
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
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("file changed after rejected save:\n%s\nvs\n%s", before, after)
	}
	if s := Load(); s.Theme != "dark" || s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("settings changed: %+v", s)
	}
}
