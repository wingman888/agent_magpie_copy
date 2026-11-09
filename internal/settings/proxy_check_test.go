package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyCheckAcceptsValidValues(t *testing.T) {
	for _, v := range []string{
		"", "   ", "direct", " direct ",
		"http://127.0.0.1:7890", "https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080",
		"127.0.0.1:7890", "[::1]:7890", "http://[::1]:7890",
	} {
		if err := CheckProxy(v); err != nil {
			t.Errorf("CheckProxy(%q) = %v, want nil", v, err)
		}
	}
}

func TestProxyCheckRejectsInvalidValues(t *testing.T) {
	for _, v := range []string{
		"ftp://x", "http://", "http://:7890", "127.0.0.1", "http://127.0.0.1",
		"http://127.0.0.1:0", "http://127.0.0.1:99999", "socks5://",
		"not a proxy", "://x:1", "127.0.0.1:port", "http://h:1/path",
	} {
		err := CheckProxy(v)
		if err == nil {
			t.Errorf("CheckProxy(%q) accepted", v)
			continue
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Errorf("CheckProxy(%q) error lacks example: %v", v, err)
		}
	}
}

func TestProxySaveValidatesAndKeepsFileOnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Theme: "dark", Proxy: "  socks5://127.0.0.1:1080  "}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "socks5://127.0.0.1:1080" {
		t.Fatalf("saved proxy = %q, want trimmed value", got)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	err = Save(Settings{Theme: "light", Proxy: "ftp://x"})
	if err == nil {
		t.Fatal("ftp proxy accepted")
	}
	if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
		t.Fatalf("error lacks example: %v", err)
	}
	after, _ := os.ReadFile(filepath.Clean(Path()))
	if string(before) != string(after) {
		t.Fatal("failed save changed the settings file")
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatalf("bare host:port rejected: %v", err)
	}
	if got := Load().Proxy; got != "127.0.0.1:7890" {
		t.Fatalf("bare host:port saved as %q", got)
	}
	if err := Save(Settings{Proxy: "direct"}); err != nil {
		t.Fatalf("direct rejected: %v", err)
	}
	if err := Save(Settings{}); err != nil || Load().Proxy != "" {
		t.Fatal("empty proxy should be accepted and stay empty")
	}
}
