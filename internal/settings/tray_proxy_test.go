package settings

import "testing"

func TestTrayValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if Load().Tray != "panel" {
		t.Fatalf("default tray: %q", Load().Tray)
	}
	for _, v := range []string{"", "panel", "window"} {
		if err := Save(Settings{Tray: v}); err != nil {
			t.Fatalf("tray %q rejected: %v", v, err)
		}
	}
	if err := Save(Settings{Tray: "menu"}); err == nil {
		t.Fatal("tray menu accepted")
	}
	if err := Save(Settings{Tray: "Window"}); err == nil {
		t.Fatal("tray is case sensitive; Window accepted")
	}
	if err := Save(Settings{Tray: "window"}); err != nil || Load().Tray != "window" {
		t.Fatalf("window not saved: %v %+v", err, Load())
	}
}

func TestProxyValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ok := map[string]string{
		"":                             "",
		"direct":                       "direct",
		"socks5://127.0.0.1:1080":      "socks5://127.0.0.1:1080",
		"socks5h://proxy.local:1080":   "socks5h://proxy.local:1080",
		"https://proxy.example:443":    "https://proxy.example:443",
		"http://user:pw@10.0.0.2:3128": "http://user:pw@10.0.0.2:3128",
		"127.0.0.1:7890":               "http://127.0.0.1:7890",
		"  localhost:7890  ":           "http://localhost:7890",
	}
	for in, want := range ok {
		if err := Save(Settings{Proxy: in}); err != nil {
			t.Fatalf("proxy %q rejected: %v", in, err)
		}
		if got := Load().Proxy; got != want {
			t.Fatalf("proxy %q stored as %q, want %q", in, got, want)
		}
	}
	bad := []string{
		"ftp://x",
		"ftp://x:21",
		"socks4://127.0.0.1:1080",
		"127.0.0.1",           // bare host needs a port
		"http://proxy:7890/x", // a path means it is not a proxy address
		"http://proxy:7890?a=b",
		"http://:7890",
		"gopher://x:70",
	}
	for _, in := range bad {
		if err := Save(Settings{Proxy: in}); err == nil {
			t.Fatalf("proxy %q accepted", in)
		}
	}
}
