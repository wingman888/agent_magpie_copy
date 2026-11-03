package settings

import (
	"os"
	"testing"
)

func TestTrayValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("tray default: %q", s.Tray)
	}
	for _, v := range []string{"", "panel", "window"} {
		if err := Save(Settings{Tray: v}); err != nil {
			t.Fatalf("tray %q rejected: %v", v, err)
		}
	}
	if err := Save(Settings{Tray: "dock"}); err == nil {
		t.Fatal("tray dock accepted")
	}
	if err := Save(Settings{Tray: "Window"}); err == nil {
		t.Fatal("tray is case sensitive")
	}
	if err := Save(Settings{Tray: "window"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "window" {
		t.Fatalf("tray not saved: %+v", s)
	}
}

func TestProxyValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ok := []string{"", "direct", "DIRECT", "socks5://127.0.0.1:1080", "127.0.0.1:7890",
		"http://proxy.corp:3128", "https://proxy.corp:443", "socks5h://h:1080", "http://user:pw@h:8080"}
	for _, p := range ok {
		if err := Save(Settings{Proxy: p}); err != nil {
			t.Errorf("proxy %q rejected: %v", p, err)
		}
	}
	bad := []string{"ftp://x", "socks4://127.0.0.1:1080", "http://", "http://h/path", "http://h?x=1", "://h:1"}
	for _, p := range bad {
		if err := Save(Settings{Proxy: p}); err == nil {
			t.Errorf("proxy %q accepted", p)
		}
	}
}

func TestProxyNormalizedOnLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "  Direct "}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Proxy != ProxyDirect {
		t.Fatalf("direct not normalized: %q", s.Proxy)
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("proxy not kept: %+v", s)
	}
	if _, err := os.Stat(Path()); err != nil {
		t.Fatal(err)
	}
}

func TestParseProxySchemes(t *testing.T) {
	u, err := ParseProxy("127.0.0.1:7890")
	if err != nil || u.Scheme != "http" || u.Host != "127.0.0.1:7890" {
		t.Fatalf("bare host:port: %v %v", u, err)
	}
	u, err = ParseProxy("SOCKS5://127.0.0.1:1080")
	if err != nil || u.Scheme != "socks5" {
		t.Fatalf("socks5 scheme: %v %v", u, err)
	}
}
