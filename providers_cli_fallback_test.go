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

// fallbackSandbox points config and cache at a temp dir and adds two custom
// providers, a (the one under test) and b, each exposing one model.
func fallbackSandbox(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Chat: "https://a.example.com/v1", Key: "sk-a", Models: []string{"m1"}},
		{ID: "b", Name: "B", Chat: "https://b.example.com/v1", Key: "sk-b", Models: []string{"m2", "m3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
}

func savedFallback(t *testing.T, id string) []string {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

func TestFallbackCmdSetShowClear(t *testing.T) {
	fallbackSandbox(t)

	if err := fallbackCmd([]string{"a", "b/m2", "b/m3", "b/m2"}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, want := savedFallback(t, "a"), []string{"b/m2", "b/m3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after set: %q, want %q", got, want)
	}

	if err := fallbackCmd([]string{"a", "b/m3", "b/m2"}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got, want := savedFallback(t, "a"), []string{"b/m3", "b/m2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after replace: %q, want %q", got, want)
	}

	if err := fallbackCmd([]string{"a"}); err != nil {
		t.Fatalf("show: %v", err)
	}

	if err := fallbackCmd([]string{"a", "none"}); err != nil {
		t.Fatalf("none: %v", err)
	}
	if got := savedFallback(t, "a"); len(got) != 0 {
		t.Fatalf("after none: %q, want empty", got)
	}
}

func TestFallbackCmdUnknownModelSavesNothing(t *testing.T) {
	fallbackSandbox(t)
	if err := fallbackCmd([]string{"a", "b/m2"}); err != nil {
		t.Fatal(err)
	}
	err := fallbackCmd([]string{"a", "b/m3", "nowhere/x"})
	if err == nil {
		t.Fatal("expected an error for an unknown model")
	}
	if !strings.Contains(err.Error(), "nowhere/x") || !strings.Contains(err.Error(), "magpie models") {
		t.Fatalf("error should name the model and point at magpie models: %v", err)
	}
	if got, want := savedFallback(t, "a"), []string{"b/m2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("original setting changed: %q, want %q", got, want)
	}
}

func TestFallbackCmdUsageErrors(t *testing.T) {
	fallbackSandbox(t)
	if err := fallbackCmd(nil); err == nil {
		t.Error("missing id should be an error")
	}
	if err := fallbackCmd([]string{"nope", "b/m2"}); err == nil {
		t.Error("unknown provider id should be an error")
	}
	if err := fallbackCmd([]string{"a", "none", "b/m2"}); err == nil {
		t.Error("none mixed with models should be an error")
	}
}

func TestProvidersListShowsFallbackChain(t *testing.T) {
	fallbackSandbox(t)
	if err := fallbackCmd([]string{"a", "b/m2", "b/m3"}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := providers(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "b/m2 → b/m3") {
		t.Fatalf("providers list lacks the fallback chain:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "  B") && strings.Contains(line, "fallback") {
			t.Fatalf("provider b has no fallback but its row shows one: %q", line)
		}
	}
}

func captureStdout(t *testing.T, fn func()) string {
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
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}
