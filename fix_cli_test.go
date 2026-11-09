package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// TestFixHandoffWithoutTerminalSavesNothing: a magpie: link handed over with
// no terminal is only checked and pointed at `magpie import`; nothing is saved
// and the key is not echoed back.
func TestFixHandoffWithoutTerminalSavesNothing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if interactive() {
		t.Skip("stdin is a terminal")
	}
	link := "magpie://import?name=Fix%20Relay&chat=https://relay.example.com/v1&key=sk-fixsecret123"
	err := handoffLink(link)
	if err == nil {
		t.Fatal("handoff without a terminal succeeded")
	}
	if !strings.Contains(err.Error(), "magpie import") || strings.Contains(err.Error(), "sk-fixsecret123") {
		t.Errorf("unexpected message: %q", err)
	}
	if n := len(provider.All()); n != 0 {
		t.Errorf("%d providers saved by a handoff without a terminal", n)
	}
	if err := handoffLink("magpie://delete?id=x"); err == nil {
		t.Error("non-import action accepted")
	}
}

// TestFixProvidersEmptyStoreRuns: with no providers, `magpie providers` still
// reports the empty state and returns cleanly (it no longer returns early
// before listing removed accounts).
func TestFixProvidersEmptyStoreRuns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := providers(); err != nil {
		t.Fatalf("providers() on empty store: %v", err)
	}
	if _, err := os.Stat(provider.Path()); err == nil {
		t.Error("providers() wrote a provider file")
	}
}
