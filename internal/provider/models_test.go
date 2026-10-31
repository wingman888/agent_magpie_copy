package provider

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestRejectsTemperatureFromFetchedList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)

	no, yes := false, true
	if err := catalog.SaveLive("p", "http://x", []catalog.Model{
		{ID: "strict", Name: "Strict", Temperature: &no},
		{ID: "plain", Name: "Plain", Temperature: &yes},
		{ID: "silent", Name: "Silent"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "p"}
	for model, want := range map[string]bool{"strict": true, "plain": false, "silent": false, "unknown": false} {
		if got := p.RejectsTemperature(model); got != want {
			t.Errorf("RejectsTemperature(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestIsOpenCode(t *testing.T) {
	for base, want := range map[string]bool{
		"https://opencode.ai/zen/go/v1": true,
		"https://api.opencode.ai/v1":    true,
		"https://notopencode.ai/v1":     false,
		"https://api.deepseek.com/v1":   false,
	} {
		if got := (Provider{Chat: base}).IsOpenCode(); got != want {
			t.Errorf("%s: %v", base, got)
		}
	}
}
