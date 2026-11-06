package settings

import (
	"testing"
)

func TestTray(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("default tray: %q", s.Tray)
	}
	if err := Save(Settings{Tray: "window"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "window" {
		t.Fatalf("saved tray: %q", s.Tray)
	}
	if err := Save(Settings{Tray: "panel"}); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("saved panel: %q", s.Tray)
	}
	// empty means the default, so it is accepted too
	if err := Save(Settings{}); err != nil {
		t.Fatalf("empty tray rejected: %v", err)
	}
	if s := Load(); s.Tray != "panel" {
		t.Fatalf("empty tray: %q", s.Tray)
	}
	for _, bad := range []string{"menu", "Window", "both"} {
		if err := Save(Settings{Tray: bad}); err == nil {
			t.Fatalf("tray %q accepted", bad)
		}
	}
}
