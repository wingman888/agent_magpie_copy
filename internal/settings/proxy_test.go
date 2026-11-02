package settings

import (
	"os"
	"strings"
	"testing"
)

func TestProxySave(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, ok := range []string{"", "direct", "http://127.0.0.1:7890", "https://proxy.lan:443", "socks5://127.0.0.1:1080", "socks5h://u:p@127.0.0.1:1080", "127.0.0.1:7890", "proxy.lan:3128", "HTTP://[::1]:8080/"} {
		if err := Save(Settings{Proxy: ok}); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	if err := Save(Settings{Theme: "dark", Proxy: "  127.0.0.1:7890\n"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("not trimmed: %q", s.Proxy)
	}
	u, err := ParseProxy(Load().Proxy)
	if err != nil || u.String() != "http://127.0.0.1:7890" {
		t.Fatalf("bare host:port: %v %v", u, err)
	}
	before, _ := os.ReadFile(Path())
	for _, bad := range []string{"ftp://x", "http://", "socks5://:1080", "not a url", "::::", "%zz", "http://h:port", "127.0.0.1", "x:", "http://h:1/path"} {
		err := Save(Settings{Proxy: bad})
		if err == nil {
			t.Errorf("%q accepted", bad)
			continue
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Errorf("%q: error has no example: %v", bad, err)
		}
	}
	if after, _ := os.ReadFile(Path()); string(after) != string(before) {
		t.Fatalf("failed save changed the file:\n%s\n%s", before, after)
	}
}

func TestProxyLoadTolerant(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"theme":"dark","proxy":"ftp://x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Theme != "dark" || s.Proxy != "ftp://x" {
		t.Fatalf("%+v", s)
	}
	if u, err := ParseProxy("direct"); u != nil || err != nil {
		t.Fatal("direct")
	}
}
