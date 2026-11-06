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

// isolateConfig points magpie at an empty home with one custom provider
// "relay" that exposes m1 and m2.
func isolateConfig(t *testing.T) {
	t.Helper()
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
}

// fallbackOf reads the saved fallback list of a provider.
func fallbackOf(t *testing.T, id string) []string {
	t.Helper()
	p, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

// captureStdout runs f and returns what it printed.
func captureStdout(t *testing.T, f func() error) (string, error) {
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
	ferr := f()
	os.Stdout = old
	w.Close()
	return <-done, ferr
}

func TestFallbackCLISetReplaceAndClear(t *testing.T) {
	isolateConfig(t)

	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "relay/m2", "relay/m1", " relay/m2 "})
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := fallbackOf(t, "relay"), []string{"relay/m2", "relay/m1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("set: got %q, want %q", got, want)
	}

	// a second call replaces the whole list, in the order given
	out, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "relay/m1"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fallbackOf(t, "relay"), []string{"relay/m1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("replace: got %q, want %q", got, want)
	}
	if !strings.Contains(out, "relay → relay/m1") {
		t.Fatalf("confirmation lacks the chain: %q", out)
	}

	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "none"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "relay"); len(got) != 0 {
		t.Fatalf("none did not clear: %q", got)
	}
}

func TestFallbackCLIUnknownModelSavesNothing(t *testing.T) {
	isolateConfig(t)
	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "relay/m1"})
	}); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{
		{"provider", "fallback", "relay", "nowhere/x"},
		{"provider", "fallback", "relay", "relay/m2", "nowhere/x"},
		{"provider", "fallback", "relay", "m2"},
		{"provider", "fallback", "relay", "relay/"},
	} {
		_, err := captureStdout(t, func() error { return providerCmd(args) })
		if err == nil {
			t.Fatalf("%v: no error", args)
		}
		if got, want := fallbackOf(t, "relay"), []string{"relay/m1"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%v changed the saved list to %q", args, got)
		}
	}
	_, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "nowhere/x"})
	})
	if err == nil || !strings.Contains(err.Error(), "nowhere/x") || !strings.Contains(err.Error(), "magpie models") {
		t.Fatalf("error should name the model and point at magpie models: %v", err)
	}
}

func TestFallbackCLIShowAndUsage(t *testing.T) {
	isolateConfig(t)

	out, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "没有 fallback") || !strings.Contains(out, "magpie provider fallback") {
		t.Fatalf("empty show: %q", out)
	}

	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "relay/m2"})
	}); err != nil {
		t.Fatal(err)
	}
	out, err = captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "relay → relay/m2") {
		t.Fatalf("show: %q", out)
	}
	if got := fallbackOf(t, "relay"); !reflect.DeepEqual(got, []string{"relay/m2"}) {
		t.Fatalf("show changed the list: %q", got)
	}

	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback"})
	}); err == nil {
		t.Fatal("missing id: no error")
	}
	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "ghost", "relay/m1"})
	}); err == nil {
		t.Fatal("unknown id: no error")
	}
	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "none", "relay/m1"})
	}); err == nil {
		t.Fatal("none mixed with a model: no error")
	}
	if got := fallbackOf(t, "relay"); !reflect.DeepEqual(got, []string{"relay/m2"}) {
		t.Fatalf("rejected commands changed the list: %q", got)
	}
}

func TestFallbackChain(t *testing.T) {
	if got := fallbackChain(provider.Provider{ID: "a"}); got != "" {
		t.Fatalf("no fallback: %q", got)
	}
	if got := fallbackChain(provider.Provider{ID: "a", Fallback: []string{"b/x", "c/y"}}); got != "a → b/x → c/y" {
		t.Fatalf("chain: %q", got)
	}
}

func TestProvidersListShowsChain(t *testing.T) {
	isolateConfig(t)
	if err := provider.Save(provider.Provider{
		ID: "plain", Name: "Plain", Chat: "https://plain.example.com/v1", Key: "sk-plain", Models: []string{"p1"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error {
		return providerCmd([]string{"provider", "fallback", "relay", "plain/p1"})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	var relayLine, plainLine string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(l, "relay.example.com"):
			relayLine = l
		case strings.Contains(l, "plain.example.com"):
			plainLine = l
		}
	}
	if !strings.Contains(relayLine, "relay → plain/p1") {
		t.Fatalf("configured row lacks the chain: %q", relayLine)
	}
	if strings.Contains(plainLine, "→") {
		t.Fatalf("unconfigured row shows a chain: %q", plainLine)
	}
}
