package main

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// cliHome points magpie's config at a fresh temp home with no signed-in
// agents, and seeds two custom providers: relay (models m1, m2) and other
// (model x1).
func cliHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-relay", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Chat: "https://other.example.com/v1", Key: "sk-other", Models: []string{"x1"}}); err != nil {
		t.Fatal(err)
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

func TestFallbackSaveNormalizesAndKeepsOrder(t *testing.T) {
	cliHome(t)
	out, err := captureStdout(t, func() error {
		return fallbackCmd([]string{"relay", " other/x1 ", "relay/m2", "other/x1"})
	})
	if err != nil {
		t.Fatalf("fallbackCmd: %v", err)
	}
	want := []string{"other/x1", "relay/m2"}
	if got := fallbackOf(t, "relay"); !reflect.DeepEqual(got, want) {
		t.Fatalf("saved fallback = %q, want %q", got, want)
	}
	if !strings.Contains(out, "other/x1 → relay/m2") {
		t.Fatalf("confirmation lacks the chain: %q", out)
	}
}

func TestFallbackUnknownModelRejectsAndChangesNothing(t *testing.T) {
	cliHome(t)
	if err := fallbackCmd([]string{"relay", "other/x1"}); err != nil {
		t.Fatal(err)
	}
	before := fallbackOf(t, "relay")

	err := fallbackCmd([]string{"relay", "relay/m2", "nowhere/x"})
	if err == nil {
		t.Fatal("unknown model accepted")
	}
	if !strings.Contains(err.Error(), "nowhere/x") || !strings.Contains(err.Error(), "magpie models") {
		t.Fatalf("error should name the model and point at magpie models: %v", err)
	}
	if got := fallbackOf(t, "relay"); !reflect.DeepEqual(got, before) {
		t.Fatalf("fallback changed on error: %q, want %q", got, before)
	}
}

func TestFallbackNoneClears(t *testing.T) {
	cliHome(t)
	if err := fallbackCmd([]string{"relay", "other/x1"}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return fallbackCmd([]string{"relay", "none"}) })
	if err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "relay"); len(got) != 0 {
		t.Fatalf("fallback after none = %q, want empty", got)
	}
	if !strings.Contains(out, "cleared") {
		t.Fatalf("no clear confirmation: %q", out)
	}
}

func TestFallbackNoneWithSpacesClears(t *testing.T) {
	cliHome(t)
	if err := fallbackCmd([]string{"relay", "other/x1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := captureStdout(t, func() error { return fallbackCmd([]string{"relay", " none "}) }); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "relay"); len(got) != 0 {
		t.Fatalf("fallback after \" none \" = %q, want empty", got)
	}
}

func TestFallbackNoneMixedWithModelsIsAnError(t *testing.T) {
	cliHome(t)
	if err := fallbackCmd([]string{"relay", "other/x1", "none"}); err == nil {
		t.Fatal("none beside a model accepted")
	}
	if got := fallbackOf(t, "relay"); len(got) != 0 {
		t.Fatalf("fallback changed on error: %q", got)
	}
}

func TestFallbackViewOnly(t *testing.T) {
	cliHome(t)
	out, err := captureStdout(t, func() error { return fallbackCmd([]string{"relay"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "没有 fallback") || !strings.Contains(out, "magpie provider fallback") {
		t.Fatalf("empty view should say there is no fallback and show usage: %q", out)
	}

	if err := fallbackCmd([]string{"relay", "other/x1", "relay/m1"}); err != nil {
		t.Fatal(err)
	}
	out, err = captureStdout(t, func() error { return fallbackCmd([]string{"relay"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "other/x1 → relay/m1") {
		t.Fatalf("view should show the chain: %q", out)
	}
	if got := fallbackOf(t, "relay"); !reflect.DeepEqual(got, []string{"other/x1", "relay/m1"}) {
		t.Fatalf("viewing changed the list: %q", got)
	}
}

func TestFallbackMissingOrUnknownID(t *testing.T) {
	cliHome(t)
	if err := fallbackCmd(nil); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("missing id: want usage error, got %v", err)
	}
	if err := fallbackCmd([]string{"ghost", "other/x1"}); err == nil {
		t.Fatal("unknown provider id accepted")
	}
}

func TestProviderCmdRoutesFallback(t *testing.T) {
	cliHome(t)
	if err := providerCmd([]string{"provider", "fallback", "relay", "other/x1"}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "relay"); !reflect.DeepEqual(got, []string{"other/x1"}) {
		t.Fatalf("routed fallback = %q", got)
	}
	if !strings.Contains(providerUsage, "magpie provider fallback <id>") {
		t.Fatal("provider usage lacks the fallback verb")
	}
}

func TestProvidersListShowsChainOnlyWhenSet(t *testing.T) {
	cliHome(t)
	if err := fallbackCmd([]string{"relay", "other/x1", "relay/m2"}); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, providers)
	if err != nil {
		t.Fatal(err)
	}
	var relayLine, otherLine string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "relay.example.com"):
			relayLine = line
		case strings.Contains(line, "other.example.com"):
			otherLine = line
		}
	}
	if !strings.Contains(relayLine, "other/x1 → relay/m2") {
		t.Fatalf("configured row lacks the chain: %q", relayLine)
	}
	if strings.Contains(otherLine, "fallback") || strings.Contains(otherLine, "→") {
		t.Fatalf("unconfigured row changed: %q", otherLine)
	}
}

func TestMainHelpMentionsFallback(t *testing.T) {
	if !strings.Contains(usage, "magpie provider fallback <id>") {
		t.Fatal("main help lacks the fallback subcommand")
	}
}
