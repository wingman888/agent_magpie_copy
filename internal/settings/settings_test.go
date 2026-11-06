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
