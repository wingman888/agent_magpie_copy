package settings

import "testing"

func TestS4SaveProxyForms(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ok := map[string]string{
		"socks5://127.0.0.1:1080":  "socks5://127.0.0.1:1080",
		"127.0.0.1:7890":           "http://127.0.0.1:7890",
		"http://proxy.corp:8080/":  "http://proxy.corp:8080",
		"https://proxy.corp:8443":  "https://proxy.corp:8443",
		"socks5h://localhost:1080": "socks5h://localhost:1080",
		"direct":                   "direct",
		"":                         "",
		"  ":                       "",
	}
	for in, want := range ok {
		if err := Save(Settings{Proxy: in}); err != nil {
			t.Fatalf("Save(proxy %q): %v", in, err)
		}
		if got := Load().Proxy; got != want {
			t.Fatalf("proxy %q saved as %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"ftp://x", "socks4://127.0.0.1:1080", "http://", "http://h:1/path?q=1", "foo:bar", "://x"} {
		if err := Save(Settings{Proxy: bad}); err == nil {
			t.Fatalf("proxy %q accepted", bad)
		}
	}
}

func TestS4SaveTray(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("default tray %q, want panel", s.Tray)
	}
	for _, v := range []string{"panel", "window", ""} {
		if err := Save(Settings{Tray: v}); err != nil {
			t.Fatalf("tray %q rejected: %v", v, err)
		}
	}
	if err := Save(Settings{Tray: "menu"}); err == nil {
		t.Fatal("tray menu accepted")
	}
	if err := Save(Settings{Tray: "Window"}); err == nil {
		t.Fatal("tray is case sensitive, Window must be rejected")
	}
	if err := Save(Settings{Tray: "window"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "window" {
		t.Fatalf("saved tray %q", s.Tray)
	}
}

func TestS4NormalizeProxyDirect(t *testing.T) {
	if p, err := NormalizeProxy("direct"); err != nil || p != Direct {
		t.Fatalf("direct: %q %v", p, err)
	}
	if _, err := NormalizeProxy("ftp://x"); err == nil {
		t.Fatal("ftp accepted")
	}
}
