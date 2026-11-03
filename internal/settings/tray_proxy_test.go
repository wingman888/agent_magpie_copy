package settings

import (
	"os"
	"testing"
)

func TestTrayValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("default tray: %q", s.Tray)
	}
	for _, v := range []string{"", "panel", "window"} {
		if err := Save(Settings{Tray: v}); err != nil {
			t.Fatalf("tray %q rejected: %v", v, err)
		}
	}
	if Save(Settings{Tray: "PANEL"}) == nil || Save(Settings{Tray: "menu"}) == nil {
		t.Fatal("bad tray accepted")
	}
	if err := Save(Settings{Tray: "window"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "window" {
		t.Fatalf("saved tray: %q", s.Tray)
	}
}

func TestProxyValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	good := []string{"", "direct", "socks5://127.0.0.1:1080", "socks5h://h.example:1080",
		"127.0.0.1:7890", "http://proxy.example:8080", "https://proxy.example:8443", " direct "}
	for _, p := range good {
		if err := Save(Settings{Proxy: p}); err != nil {
			t.Errorf("proxy %q rejected: %v", p, err)
		}
	}
	bad := []string{"ftp://x", "x", "127.0.0.1", "socks4://127.0.0.1:1080",
		"http://h.example/path", "http://:80", "host:99999", "Direct", "http://h.example?x=1"}
	for _, p := range bad {
		if err := Save(Settings{Proxy: p}); err == nil {
			t.Errorf("proxy %q accepted", p)
		}
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("saved proxy: %q", s.Proxy)
	}
	if _, err := os.Stat(Path()); err != nil {
		t.Fatal(err)
	}
}
