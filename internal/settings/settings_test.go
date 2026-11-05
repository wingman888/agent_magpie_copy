package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Theme != "system" || s.Lang != "system" {
		t.Fatalf("defaults: %+v", s)
	}
	if err := Save(Settings{Theme: "dark", Lang: "zh"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Theme != "dark" || s.Lang != "zh" {
		t.Fatalf("saved: %+v", s)
	}
	if err := Save(Settings{Theme: "sepia"}); err == nil {
		t.Fatal("bad theme accepted")
	}
	if Save(Settings{}) != nil || Load().Theme != "system" {
		t.Fatal("empty means system")
	}
	if filepath.Base(Path()) != "settings.json" {
		t.Fatal(Path())
	}
}

func TestMigrate(t *testing.T) {
	cfg, cache := t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("XDG_CACHE_HOME", cache)
	old := filepath.Join(cfg, "dial")
	if err := os.MkdirAll(filepath.Join(old, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "providers.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "sub", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	Migrate()
	fi, err := os.Stat(filepath.Join(cfg, "magpie", "providers.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("providers.json not copied with its mode: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(cfg, "magpie", "sub", "x")); err != nil {
		t.Fatal("nested file not copied")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("the old folder must stay")
	}
	if _, err := os.Stat(filepath.Join(cache, "magpie")); err == nil {
		t.Fatal("no old cache, so no new one")
	}
	// a second run must not touch an existing folder
	if err := os.WriteFile(filepath.Join(cfg, "magpie", "providers.json"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	Migrate()
	if b, _ := os.ReadFile(filepath.Join(cfg, "magpie", "providers.json")); string(b) != "new" {
		t.Fatal("existing folder overwritten")
	}
}

func TestTrayAndProxyValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" || s.Proxy != "" {
		t.Fatalf("defaults: %+v", s)
	}
	for _, good := range []string{"", "panel", "window"} {
		if err := Save(Settings{Tray: good}); err != nil {
			t.Errorf("tray %q: %v", good, err)
		}
	}
	if err := Save(Settings{Tray: "menu"}); err == nil {
		t.Error("bad tray accepted")
	}
	for _, good := range []string{"", "direct", "DIRECT", "socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080", "127.0.0.1:7890", "http://10.0.0.1:3128", "https://proxy.example:443"} {
		if err := Save(Settings{Proxy: good}); err != nil {
			t.Errorf("proxy %q: %v", good, err)
		}
	}
	if s := Load(); s.Proxy != "https://proxy.example:443" {
		t.Errorf("last saved proxy: %q", s.Proxy)
	}
	for _, bad := range []string{"ftp://x", "x:y:z", "socks4://127.0.0.1:1080", "://nohost"} {
		if err := Save(Settings{Proxy: bad}); err == nil {
			t.Errorf("proxy %q accepted", bad)
		}
	}
	if err := Save(Settings{Proxy: "direct"}); err != nil || Load().Proxy != "direct" {
		t.Fatal("direct not kept")
	}
}
