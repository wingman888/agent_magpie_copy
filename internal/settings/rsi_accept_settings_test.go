package settings_test

import (
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

func TestAcceptSettingsTrayAndProxy(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if s := settings.Load(); s.Tray != "panel" || s.Proxy != "" {
		t.Fatalf("defaults: %+v", s)
	}
	for _, tray := range []string{"panel", "window", ""} {
		if err := settings.Save(settings.Settings{Tray: tray}); err != nil {
			t.Errorf("tray %q: %v", tray, err)
		}
	}
	if err := settings.Save(settings.Settings{Tray: "window"}); err != nil {
		t.Fatal(err)
	}
	if s := settings.Load(); s.Tray != "window" {
		t.Fatalf("tray not kept: %+v", s)
	}
	if err := settings.Save(settings.Settings{Tray: "dock"}); err == nil {
		t.Error("bad tray accepted")
	}
	for _, p := range []string{"socks5://127.0.0.1:1080", "127.0.0.1:7890", "http://127.0.0.1:7890", "https://p.example:443", "socks5h://127.0.0.1:1080", "direct", ""} {
		if err := settings.Save(settings.Settings{Proxy: p}); err != nil {
			t.Errorf("proxy %q: %v", p, err)
		}
	}
	if err := settings.Save(settings.Settings{Proxy: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatal(err)
	}
	if settings.Load().Proxy != "socks5://127.0.0.1:1080" {
		t.Errorf("proxy not kept: %+v", settings.Load())
	}
	for _, p := range []string{"ftp://x", "ftp://x:21"} {
		if err := settings.Save(settings.Settings{Proxy: p}); err == nil {
			t.Errorf("proxy %q accepted", p)
		}
	}
	// a rejected save leaves the file alone
	if settings.Load().Proxy != "socks5://127.0.0.1:1080" {
		t.Errorf("rejected save changed settings: %+v", settings.Load())
	}
	if _, err := os.Stat(settings.Path()); err != nil {
		t.Fatal(err)
	}
}
