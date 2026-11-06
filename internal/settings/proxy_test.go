package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveProxyValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	bad := []string{
		"ftp://x",
		"http://",
		"http://:7890",
		"socks5:///path",
		"127.0.0.1",
		"just a string",
		"host:notaport",
		"host:0",
		"host:70000",
		":7890",
	}
	for _, p := range bad {
		err := Save(Settings{Proxy: p})
		if err == nil {
			t.Errorf("proxy %q accepted", p)
			continue
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Errorf("proxy %q: error lacks example: %v", p, err)
		}
	}

	good := map[string]string{
		"":                         "",
		"direct":                   "direct",
		"127.0.0.1:7890":           "127.0.0.1:7890",
		"http://127.0.0.1:7890":    "http://127.0.0.1:7890",
		"https://proxy.local:443":  "https://proxy.local:443",
		"socks5://127.0.0.1:1080":  "socks5://127.0.0.1:1080",
		"socks5h://127.0.0.1:1080": "socks5h://127.0.0.1:1080",
		"  127.0.0.1:7890 \t":      "127.0.0.1:7890",
		"\n direct \n":             "direct",
	}
	for in, want := range good {
		if err := Save(Settings{Proxy: in}); err != nil {
			t.Errorf("proxy %q rejected: %v", in, err)
			continue
		}
		if got := Load().Proxy; got != want {
			t.Errorf("proxy %q saved as %q, want %q", in, got, want)
		}
	}
}

func TestSaveProxyKeepsFileOnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := Save(Settings{Theme: "dark", Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "magpie", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{Theme: "light", Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp proxy accepted")
	}
	after, err := os.ReadFile(filepath.Join(dir, "magpie", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("file changed on failed save:\n%s\nvs\n%s", before, after)
	}
	if s := Load(); s.Theme != "dark" || s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("settings changed: %+v", s)
	}
}

func TestProxyOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := Save(Settings{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "magpie", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "proxy") {
		t.Fatalf("empty proxy written: %s", b)
	}
}
