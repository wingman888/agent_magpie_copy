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

func TestProxySave(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Save(Settings{Proxy: "  direct  "}); err != nil {
		t.Fatal("direct rejected:", err)
	}
	if s := Load(); s.Proxy != "direct" {
		t.Fatalf("direct not saved trimmed: %q", s.Proxy)
	}
	if err := Save(Settings{}); err != nil {
		t.Fatal("empty rejected:", err)
	}
	if err := Save(Settings{Proxy: "127.0.0.1:7890"}); err != nil {
		t.Fatal("bare host:port rejected:", err)
	}
	if s := Load(); s.Proxy != "127.0.0.1:7890" {
		t.Fatalf("bare host:port not saved: %q", s.Proxy)
	}
	for _, v := range []string{"http://127.0.0.1:7890", "https://p:443", "socks5://127.0.0.1:1080", "socks5h://h:1080"} {
		if err := Save(Settings{Proxy: v}); err != nil {
			t.Fatalf("%s rejected: %v", v, err)
		}
	}
}

func TestProxyRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := Save(Settings{Theme: "dark", Proxy: "http://127.0.0.1:7890"}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"ftp://x", "http://", "socks5://", "://bad", "::::", "just-a-word", "127.0.0.1", "http://h:99999"} {
		err := Save(Settings{Theme: "light", Proxy: v})
		if err == nil {
			t.Fatalf("%q accepted", v)
		}
		if !strings.Contains(err.Error(), "http://127.0.0.1:7890") {
			t.Fatalf("%q: error lacks example: %v", v, err)
		}
	}
	after, err := os.ReadFile(Path())
	if err != nil || string(after) != string(before) {
		t.Fatalf("file changed after rejected save: %v", err)
	}
	if s := Load(); s.Theme != "dark" || s.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("settings changed: %+v", s)
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
