package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProxy(t *testing.T) {
	ok := []string{"", "direct", "http://127.0.0.1:7890", "https://proxy.local:443",
		"socks5://127.0.0.1:1080", "socks5h://[::1]:1080", "127.0.0.1:7890", "proxy.corp:8080"}
	for _, s := range ok {
		if err := ValidateProxy(s); err != nil {
			t.Errorf("%q rejected: %v", s, err)
		}
	}
	bad := []string{"ftp://x", "http://", "socks5://:1080", "http://127.0.0.1:99999",
		"127.0.0.1", ":7890", "127.0.0.1:port", "not a proxy", "://x"}
	for _, s := range bad {
		if err := ValidateProxy(s); err == nil {
			t.Errorf("%q accepted", s)
		} else if !strings.Contains(err.Error(), ProxyExample) {
			t.Errorf("%q: error lacks example: %v", s, err)
		}
	}
}

func TestSaveProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "  127.0.0.1:7890  "}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "127.0.0.1:7890" {
		t.Fatalf("proxy = %q, want trimmed value", got)
	}
	if err := Save(Settings{Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if got := Load().Proxy; got != "direct" {
		t.Fatalf("proxy = %q", got)
	}
	if err := Save(Settings{}); err != nil || Load().Proxy != "" {
		t.Fatal("empty proxy must save")
	}
}

func TestSaveBadProxyKeepsFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Theme: "dark", Proxy: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
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
	after, _ := os.ReadFile(filepath.Join(filepath.Dir(Path()), "settings.json"))
	if string(before) != string(after) {
		t.Fatalf("file changed after failed save:\n%s\n%s", before, after)
	}
}
