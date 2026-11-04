package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestFallbackCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Models: []string{"m1", "m2"},
	}); err != nil {
		t.Fatal(err)
	}
	fallbackOf := func() []string {
		p, err := provider.Find("relay")
		if err != nil {
			t.Fatal(err)
		}
		return p.Fallback
	}

	// an unknown model is refused and nothing is saved
	if err := fallbackCmd([]string{"relay", "relay/m2", "nowhere/x"}); err == nil {
		t.Fatal("fallbackCmd with unknown model: want error")
	}
	if got := fallbackOf(); got != nil {
		t.Fatalf("after rejected update Fallback = %q, want none", got)
	}

	// a valid list is saved in the order given, with duplicates dropped
	if err := fallbackCmd([]string{"relay", "relay/m2", "relay/m1", "relay/m2"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"relay/m2", "relay/m1"}; !reflect.DeepEqual(fallbackOf(), want) {
		t.Fatalf("Fallback = %q, want %q", fallbackOf(), want)
	}

	// an invalid update leaves the existing list alone
	if err := fallbackCmd([]string{"relay", "nowhere/x"}); err == nil {
		t.Fatal("want error for unknown model")
	}
	if want := []string{"relay/m2", "relay/m1"}; !reflect.DeepEqual(fallbackOf(), want) {
		t.Fatalf("after rejected update Fallback = %q, want %q", fallbackOf(), want)
	}

	// none clears it
	if err := fallbackCmd([]string{"relay", "none"}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(); got != nil {
		t.Fatalf("after none Fallback = %q, want none", got)
	}

	// a missing or unknown id is an error
	if err := fallbackCmd(nil); err == nil {
		t.Fatal("no id: want usage error")
	}
	if err := fallbackCmd([]string{"nobody"}); err == nil {
		t.Fatal("unknown id: want error")
	}
}
