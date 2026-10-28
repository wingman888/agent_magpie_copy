package settings

import (
	"os"
	"path/filepath"
	"strings"
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

func TestProxySave(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, p := range []string{"http://h:1", "https://h:1", "socks5://h:1", "socks5h://h:1", "127.0.0.1:7890", "direct", ""} {
		if err := Save(Settings{Proxy: p}); err != nil {
			t.Fatal(p, err)
		}
	}
	if err := Save(Settings{Proxy: "  direct  "}); err != nil || Load().Proxy != "direct" {
		t.Fatal(err, Load().Proxy)
	}
	if err := Save(Settings{Proxy: " 127.0.0.1:7890 "}); err != nil || Load().Proxy != "127.0.0.1:7890" {
		t.Fatal(err, Load().Proxy)
	}
	for _, p := range []string{"ftp://x", "http://", "://bad"} {
		err := Save(Settings{Theme: "dark", Proxy: p})
		if err == nil || !strings.HasPrefix(err.Error(), "proxy must look like") {
			t.Fatal(p, err)
		}
		if got := Load(); got.Proxy != "127.0.0.1:7890" || got.Theme != "system" {
			t.Fatalf("%s: %+v", p, got)
		}
	}
}
