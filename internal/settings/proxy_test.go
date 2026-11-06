package settings

import "testing"

func TestValidProxy(t *testing.T) {
	ok := []string{"", "direct", "socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080", "http://proxy.example:8080", "https://proxy.example:443", "127.0.0.1:7890"}
	for _, p := range ok {
		if err := ValidProxy(p); err != nil {
			t.Errorf("%q rejected: %v", p, err)
		}
	}
	bad := []string{"ftp://x", "socks4://127.0.0.1:1080", "http://", "127.0.0.1", "host:notaport", "http://h:99999", "just words"}
	for _, p := range bad {
		if err := ValidProxy(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
}

func TestSaveTrayAndProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" || s.Proxy != "" {
		t.Fatalf("defaults: %+v", s)
	}
	if err := Save(Settings{Tray: "window", Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "window" || s.Proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("saved: %+v", s)
	}
	if err := Save(Settings{Tray: "menu"}); err == nil {
		t.Fatal("bad tray accepted")
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Settings{Proxy: "ftp://x"}); err == nil {
		t.Fatal("ftp proxy accepted")
	}
	if err := Save(Settings{Tray: "", Proxy: "direct"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "panel" || s.Proxy != "direct" {
		t.Fatalf("empty tray means panel: %+v", s)
	}
}
