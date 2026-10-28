package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// run captures what a command prints.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := providerCmd(append([]string{"provider"}, args...))
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), err
}

func TestProviderFallbackCommand(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "c", "main"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Chat: "https://x.test/v1", Models: []string{"b", "d"}}); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := run(t, "fallback", "main"); err != nil || !strings.Contains(out, "has no fallback") {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := run(t, "fallback", "main", "a/b", "c/d"); err != nil || !strings.Contains(out, "falls back to a/b → c/d") {
		t.Fatalf("%q %v", out, err)
	}
	_, err := run(t, "fallback", "main", "a/b", "nowhere/x")
	if err == nil || err.Error() != `magpie knows no model "nowhere/x" (magpie models lists them)` {
		t.Fatalf("%v", err)
	}
	if out, _ := run(t, "fallback", "main"); !strings.Contains(out, "a/b → c/d") {
		t.Fatalf("list changed: %q", out)
	}
	if _, err := run(t, "fallback"); err == nil || err.Error() != "magpie provider fallback <id> [provider/model… | none]" {
		t.Fatalf("%v", err)
	}
	if out, err := run(t, "fallback", "main", "none"); err != nil || !strings.Contains(out, "has no fallback") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := run(t, "fallback", "ghost"); err == nil {
		t.Fatal("want error for unknown provider")
	}
}
