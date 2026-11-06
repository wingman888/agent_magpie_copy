package settings

import (
	"os"
	"strings"
	"testing"
)

func TestProxySave(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	valid := map[string]string{
		"":                         "",
		"direct":                   "direct",
		"127.0.0.1:7890":           "127.0.0.1:7890",
		"http://127.0.0.1:7890":    "http://127.0.0.1:7890",
		"https://proxy.local:443":  "https://proxy.local:443",
		"socks5://127.0.0.1:1080":  "socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080": "socks5h://127.0.0.1:1080",
		"  http://x:1  ":           "http://x:1",
	}
	for in, want := range valid {
		if err := Save(Settings{Proxy: in}); err != nil {
			t.Fatalf("Save(%q): %v", in, err)
		}
		if got := Load().Proxy; got != want {
			t.Fatalf("Save(%q) stored %q, want %q", in, got, want)
		}
	}
}

func TestProxySaveRejectsInvalid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "http://a:1"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"ftp://x", "http://", "socks4://127.0.0.1:1080", "not a proxy::"} {
		err := Save(Settings{Proxy: bad, Theme: "dark"})
		if err == nil {
			t.Fatalf("Save(%q) accepted", bad)
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Fatalf("Save(%q) error lacks example: %v", bad, err)
		}
	}
	after, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("failed Save changed the file:\n%s\n%s", before, after)
	}
	if Load().Proxy != "http://a:1" {
		t.Fatalf("proxy changed: %q", Load().Proxy)
	}
}
