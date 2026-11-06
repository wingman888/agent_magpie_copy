package main

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// fallbackHome gives the CLI a fresh config home with two custom providers:
// a (serves m1) and b (serves m2).
func fallbackHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k", Models: []string{"m1"}},
		{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "k", Models: []string{"m2"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

// captureStdout runs f and returns what it printed to stdout.
func captureStdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	runErr := f()
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	r.Close()
	return string(b), runErr
}

func fallbackOf(t *testing.T, id string) []string {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

func TestProviderFallbackSetAndClear(t *testing.T) {
	fallbackHome(t)
	out, err := captureStdout(t, func() error { return fallbackCmd([]string{"a", "b/m2", "a/m1"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fallbackOf(t, "a"), []string{"b/m2", "a/m1"}) {
		t.Fatalf("saved %q", fallbackOf(t, "a"))
	}
	if !strings.Contains(out, "b/m2 → a/m1") {
		t.Fatalf("confirmation lacks chain: %q", out)
	}

	// the list shows the chain too
	out, err = captureStdout(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "b/m2 → a/m1") {
		t.Fatalf("providers list lacks chain: %q", out)
	}

	// showing only the id
	out, err = captureStdout(t, func() error { return fallbackCmd([]string{"a"}) })
	if err != nil || !strings.Contains(out, "b/m2 → a/m1") {
		t.Fatalf("show: %q %v", out, err)
	}

	// none clears
	if _, err := captureStdout(t, func() error { return fallbackCmd([]string{"a", "none"}) }); err != nil {
		t.Fatal(err)
	}
	if len(fallbackOf(t, "a")) != 0 {
		t.Fatalf("not cleared: %q", fallbackOf(t, "a"))
	}
	out, err = captureStdout(t, func() error { return fallbackCmd([]string{"a"}) })
	if err != nil || !strings.Contains(out, "没有 fallback") || !strings.Contains(out, "magpie provider fallback") {
		t.Fatalf("empty show: %q %v", out, err)
	}
}

func TestProviderFallbackRejectsUnknownAndKeepsOld(t *testing.T) {
	fallbackHome(t)
	if _, err := captureStdout(t, func() error { return fallbackCmd([]string{"a", "b/m2"}) }); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"a", "nowhere/x"},
		{"a", "b/m2", "nowhere/x"},
		{"a", "b/nope"},
		{"a", "b"},
		{"a", "none", "b/m2"},
	} {
		_, err := captureStdout(t, func() error { return fallbackCmd(args) })
		if err == nil {
			t.Errorf("%v: accepted", args)
			continue
		}
		if !reflect.DeepEqual(fallbackOf(t, "a"), []string{"b/m2"}) {
			t.Fatalf("%v changed the saved list to %q", args, fallbackOf(t, "a"))
		}
	}
	_, err := captureStdout(t, func() error { return fallbackCmd([]string{"a", "nowhere/x"}) })
	if err == nil || !strings.Contains(err.Error(), "nowhere/x") || !strings.Contains(err.Error(), "magpie models") {
		t.Fatalf("unknown model error: %v", err)
	}
}

func TestProviderFallbackUsage(t *testing.T) {
	fallbackHome(t)
	if err := fallbackCmd(nil); err == nil || !strings.Contains(err.Error(), "magpie provider fallback") {
		t.Fatalf("no id: %v", err)
	}
	if err := fallbackCmd([]string{"missing", "a/m1"}); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if !strings.Contains(providerUsage, "magpie provider fallback") {
		t.Fatal("provider usage lacks fallback")
	}
	if !strings.Contains(usage, "magpie provider fallback") {
		t.Fatal("main help lacks fallback")
	}
}

func TestProviderFallbackViaProviderCmd(t *testing.T) {
	fallbackHome(t)
	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "a", "b/m2"})
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fallbackOf(t, "a"), []string{"b/m2"}) {
		t.Fatalf("saved %q", fallbackOf(t, "a"))
	}
}
