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

// cliHome points magpie at an empty temp home with two custom providers.
func cliHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Chat: "https://a.example.com/v1", Key: "sk-a", Models: []string{"m1"}},
		{ID: "b", Name: "B", Chat: "https://b.example.com/v1", Key: "sk-b", Models: []string{"m2"}},
		{ID: "c", Name: "C", Chat: "https://c.example.com/v1", Key: "sk-c", Models: []string{"m3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

// stdout runs f and returns what it printed.
func stdout(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	ferr := f()
	os.Stdout = old
	w.Close()
	return <-done, ferr
}

func fallbackOf(t *testing.T, id string) []string {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

func TestProviderFallbackCLI(t *testing.T) {
	cliHome(t)
	run := func(args ...string) (string, error) {
		return stdout(t, func() error { return providerCmd(append([]string{"provider", "fallback"}, args...)) })
	}

	// nothing set: view only, with a hint
	out, err := run("a")
	if err != nil || !strings.Contains(out, "no fallback") || !strings.Contains(out, "magpie provider fallback") {
		t.Fatalf("view empty: %q %v", out, err)
	}

	// set, in order
	out, err = run("a", "b/m2", "c/m3")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "b/m2 → c/m3") {
		t.Fatalf("set output: %q", out)
	}
	if got := fallbackOf(t, "a"); !reflect.DeepEqual(got, []string{"b/m2", "c/m3"}) {
		t.Fatalf("saved %q", got)
	}

	// view
	out, err = run("a")
	if err != nil || !strings.Contains(out, "b/m2 → c/m3") {
		t.Fatalf("view: %q %v", out, err)
	}

	// an unknown model is refused and nothing changes
	out, err = run("a", "c/m3", "nowhere/x")
	if err == nil || !strings.Contains(err.Error(), "nowhere/x") || !strings.Contains(err.Error(), "magpie models") {
		t.Fatalf("unknown model: %q %v", out, err)
	}
	if got := fallbackOf(t, "a"); !reflect.DeepEqual(got, []string{"b/m2", "c/m3"}) {
		t.Fatalf("changed after an error: %q", got)
	}

	// providers list shows the chain on a's row only
	out, err = stdout(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out, "\n") {
		has := strings.Contains(line, "b/m2 → c/m3")
		if strings.Contains(line, "a.example.com") != has {
			t.Fatalf("providers line %q", line)
		}
	}

	// replace wholesale
	if _, err := run("a", "c/m3"); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "a"); !reflect.DeepEqual(got, []string{"c/m3"}) {
		t.Fatalf("replaced %q", got)
	}

	// none clears
	if _, err := run("a", "none"); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "a"); len(got) != 0 {
		t.Fatalf("not cleared: %q", got)
	}
	if out, _ := stdout(t, providers); strings.Contains(out, "→") {
		t.Fatalf("providers still shows a chain: %q", out)
	}

	// missing or unknown id
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "magpie provider fallback") {
		t.Fatalf("no id: %v", err)
	}
	if _, err := run("zzz", "b/m2"); err == nil {
		t.Fatal("unknown id accepted")
	}
}

func TestFallbackInHelp(t *testing.T) {
	if !strings.Contains(usage, "magpie provider fallback") || !strings.Contains(providerUsage, "magpie provider fallback") {
		t.Fatal("fallback missing from the help")
	}
}
