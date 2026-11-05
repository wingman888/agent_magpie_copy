package settings

import (
	"testing"
)

func TestTraySetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("default tray: %+v", s)
	}
	if err := Save(Settings{Tray: "window"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "window" {
		t.Fatalf("saved tray: %+v", s)
	}
	if err := Save(Settings{Tray: "menu"}); err == nil {
		t.Fatal("bad tray accepted")
	}
	if err := Save(Settings{}); err != nil || Load().Tray != "panel" {
		t.Fatal("empty tray means panel")
	}
}

func TestProxySetting(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, ok := range []string{"", "direct", "socks5://127.0.0.1:1080", "127.0.0.1:7890", "http://h:1", "https://h:2", "socks5h://h:3"} {
		if err := Save(Settings{Proxy: ok}); err != nil {
			t.Fatalf("proxy %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"ftp://x", "x", "socks5://", "http://h:1/path?q=1", "not a url"} {
		if err := Save(Settings{Proxy: bad}); err == nil {
			t.Fatalf("proxy %q accepted", bad)
		}
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("bare host:port should be saved as http: %+v", s)
	}
	if err := Save(Settings{Proxy: "direct"}); err != nil || Load().Proxy != "direct" {
		t.Fatal("direct not kept")
	}
	if err := Save(Settings{}); err != nil || Load().Proxy != "" {
		t.Fatal("empty proxy means follow the environment")
	}
}

func TestParseProxy(t *testing.T) {
	if u, err := ParseProxy("127.0.0.1:7890"); err != nil || u.Scheme != "http" || u.Host != "127.0.0.1:7890" {
		t.Fatalf("bare: %v %v", u, err)
	}
	if u, err := ParseProxy("direct"); err != nil || u != nil {
		t.Fatalf("direct: %v %v", u, err)
	}
	if _, err := ParseProxy("ftp://x"); err == nil {
		t.Fatal("ftp accepted")
	}
}
