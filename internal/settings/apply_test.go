package settings

import (
	"strings"
	"testing"
)

func TestApplyKeepsFieldsNotInDocument(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	base := Settings{Theme: "dark", Lang: "zh", Tray: "window", Proxy: "socks5://127.0.0.1:1080"}
	got, err := Apply(base, strings.NewReader(`{"theme":"light","lang":"en"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Theme != "light" || got.Lang != "en" {
		t.Fatalf("theme/lang not applied: %+v", got)
	}
	if got.Tray != "window" || got.Proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("tray/proxy changed by a partial save: %+v", got)
	}
	if err := Save(got); err != nil {
		t.Fatal(err)
	}
	if s := Load(); s.Tray != "window" || s.Proxy != "socks5://127.0.0.1:1080" {
		t.Fatalf("saved file lost tray/proxy: %+v", s)
	}
}

func TestApplyRejectsBadJSON(t *testing.T) {
	base := Settings{Tray: "window"}
	if _, err := Apply(base, strings.NewReader(`{"tray":`)); err == nil {
		t.Fatal("truncated JSON accepted")
	}
}
