package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// cliHome points config, cache and login lookups at a temp home, so the
// provider file is fresh and no real agent sign-in is seen.
func cliHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

// stdout runs fn and returns what it printed.
func stdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	err = fn()
	os.Stdout = old
	w.Close()
	return <-done, err
}

// twoProviders saves a (models m1, m3) and b (model m2), both with keys.
func twoProviders(t *testing.T) {
	t.Helper()
	cliHome(t)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k", Models: []string{"m1", "m3"}},
		{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "k", Models: []string{"m2"}},
		{ID: "c", Name: "C", Chat: "https://c.example/v1", Key: "k", Models: []string{"m4"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

func fallbackOf(t *testing.T, id string) []string {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

func runFallback(args ...string) error {
	return providerCmd(append([]string{"provider", "fallback"}, args...))
}

func TestFallbackCommandSetShowClear(t *testing.T) {
	twoProviders(t)

	out, err := stdout(t, func() error { return runFallback("a", "b/m2", "a/m3") })
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b/m2", "a/m3"}; !reflect.DeepEqual(fallbackOf(t, "a"), want) {
		t.Fatalf("saved %q, want %q", fallbackOf(t, "a"), want)
	}
	if !strings.Contains(out, "b/m2 → a/m3") {
		t.Fatalf("confirmation lacks the chain: %q", out)
	}

	out, err = stdout(t, func() error { return runFallback("a") })
	if err != nil || !strings.Contains(out, "b/m2 → a/m3") {
		t.Fatalf("show: %q %v", out, err)
	}

	out, err = stdout(t, func() error { return runFallback("a", "none") })
	if err != nil {
		t.Fatal(err)
	}
	if len(fallbackOf(t, "a")) != 0 || !strings.Contains(out, "cleared") {
		t.Fatalf("clear: %q, stored %q", out, fallbackOf(t, "a"))
	}

	out, err = stdout(t, func() error { return runFallback("a") })
	if err != nil || !strings.Contains(out, "no fallback") || !strings.Contains(out, "magpie provider fallback") {
		t.Fatalf("show after clear: %q %v", out, err)
	}
}

func TestFallbackCommandRejectsUnknownAndKeepsSettings(t *testing.T) {
	twoProviders(t)
	if err := runFallback("a", "b/m2"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"a", "nowhere/x"},
		{"a", "b/m2", "nowhere/x"},
		{"a", "b/unknown-model"},
		{"a", "b"},
	} {
		if err := runFallback(args...); err == nil {
			t.Fatalf("%v accepted", args)
		}
		if got := fallbackOf(t, "a"); !reflect.DeepEqual(got, []string{"b/m2"}) {
			t.Fatalf("after %v: stored %q, want the old setting", args, got)
		}
	}
	if err := runFallback("a", "nowhere/x"); err == nil || !strings.Contains(err.Error(), "magpie models") {
		t.Fatalf("unknown model should point at magpie models: %v", err)
	}
}

func TestFallbackCommandUsageErrors(t *testing.T) {
	twoProviders(t)
	if err := runFallback(); err == nil || !strings.Contains(err.Error(), "magpie provider fallback <id>") {
		t.Fatalf("no id: %v", err)
	}
	if err := runFallback("nope", "b/m2"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if err := runFallback("a", "none", "b/m2"); err == nil {
		t.Fatal("none beside a model accepted")
	}
	if got := fallbackOf(t, "a"); len(got) != 0 {
		t.Fatalf("errors saved %q", got)
	}
}

func TestProvidersListShowsFallbackChain(t *testing.T) {
	twoProviders(t)
	if err := runFallback("a", "b/m2", "a/m3"); err != nil {
		t.Fatal(err)
	}
	out, err := stdout(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	var aLine, cLine string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "a.example"):
			aLine = line
		case strings.Contains(line, "c.example"):
			cLine = line
		}
	}
	if !strings.Contains(aLine, "b/m2 → a/m3") {
		t.Fatalf("a's row lacks the chain: %q\nfull:\n%s", aLine, out)
	}
	if strings.Contains(cLine, "fallback") {
		t.Fatalf("c has no fallback but its row mentions one: %q", cLine)
	}
}
