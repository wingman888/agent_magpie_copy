package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// fallbackHome points magpie's config and cache at a fresh directory and saves
// one custom provider to fall back from.
func fallbackHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://r.example/v1", Key: "sk-r"}); err != nil {
		t.Fatal(err)
	}
}

func fallbackNow(t *testing.T) []string {
	t.Helper()
	p, err := provider.Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

func TestFallbackCommandSetsAndClears(t *testing.T) {
	fallbackHome(t)
	if err := fallbackCmd([]string{"relay", "a/b", "a/b", "", "c/d"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fallbackNow(t), "|"); got != "a/b|c/d" {
		t.Fatalf("fallback after set: %q", got)
	}
	if err := fallbackCmd([]string{"relay"}); err != nil {
		t.Fatalf("view: %v", err)
	}
	if err := fallbackCmd([]string{"relay", ""}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackNow(t); len(got) != 0 {
		t.Fatalf("fallback after clearing: %q", got)
	}
}

func TestFallbackCommandRouting(t *testing.T) {
	fallbackHome(t)
	if err := providerCmd([]string{"magpie", "fallback", "relay", "x/y"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(fallbackNow(t), "|"); got != "x/y" {
		t.Fatalf("fallback via providerCmd: %q", got)
	}
	if err := fallbackCmd(nil); err == nil {
		t.Error("no id accepted")
	}
	if err := fallbackCmd([]string{"nope", "a/b"}); err == nil {
		t.Error("unknown provider accepted")
	}
}
