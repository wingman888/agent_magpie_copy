package settings

import "testing"

func TestS5TrayValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("default tray: %q", s.Tray)
	}
	if err := Save(Settings{Tray: "window"}); err != nil || Load().Tray != "window" {
		t.Fatalf("window: %v", err)
	}
	if err := Save(Settings{Tray: ""}); err != nil || Load().Tray != "panel" {
		t.Fatalf("empty means panel: %v", err)
	}
	if err := Save(Settings{Tray: "menu"}); err == nil {
		t.Fatal("bad tray accepted")
	}
}

func TestS5ProxyValues(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, ok := range []string{"", "direct", "socks5://127.0.0.1:1080", "127.0.0.1:7890", "http://proxy.example:8080", "https://proxy.example", "socks5h://h:1080"} {
		if err := Save(Settings{Proxy: ok}); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil || Load().Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("bare host:port stored as %q (%v)", Load().Proxy, err)
	}
	for _, bad := range []string{"ftp://x", "x", "socks4://h:1", "http://", "host:"} {
		if err := Save(Settings{Proxy: bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
