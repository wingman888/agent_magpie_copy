package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestF05FallbackCLISetClearAndReject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	for _, id := range []string{"f05a", "f05b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Chat: "https://example.com/v1", Key: "k"}); err != nil {
			t.Fatal(err)
		}
	}
	fallbackOf := func() []string {
		t.Helper()
		p, err := provider.Find("f05a")
		if err != nil {
			t.Fatal(err)
		}
		return p.Fallback
	}

	// replace the list, dropping duplicates and keeping order
	if err := providerCmd([]string{"provider", "fallback", "f05a", "f05b/m2", "f05b/m1", "f05b/m2"}); err != nil {
		t.Fatal(err)
	}
	if got, want := fallbackOf(), []string{"f05b/m2", "f05b/m1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback = %q, want %q", got, want)
	}

	// an unknown provider in the list saves nothing
	if err := providerCmd([]string{"provider", "fallback", "f05a", "f05c/x"}); err == nil {
		t.Fatal("expected error for unknown provider in fallback list")
	}
	if got, want := fallbackOf(), []string{"f05b/m2", "f05b/m1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after rejected edit fallback = %q, want %q", got, want)
	}

	// none clears
	if err := providerCmd([]string{"provider", "fallback", "f05a", "none"}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(); len(got) != 0 {
		t.Fatalf("fallback after none = %q, want empty", got)
	}

	// query with nothing set is not an error
	if err := providerCmd([]string{"provider", "fallback", "f05a"}); err != nil {
		t.Fatal(err)
	}

	// missing or unknown id is an error
	if err := providerCmd([]string{"provider", "fallback"}); err == nil {
		t.Fatal("expected usage error without an id")
	}
	if err := providerCmd([]string{"provider", "fallback", "f05zz", "f05b/m1"}); err == nil {
		t.Fatal("expected error for unknown id")
	}
}
