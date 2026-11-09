package main

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func fbcfgCLIIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestFbcfgProviderFallbackCLI(t *testing.T) {
	fbcfgCLIIsolate(t)
	for _, p := range []provider.Provider{
		{ID: "fbcfg-a", Name: "fbcfg A", Chat: "https://a.example/v1", Key: "sk-fbcfg-1234567890"},
		{ID: "fbcfg-b", Name: "fbcfg B", Chat: "https://b.example/v1", Key: "sk-fbcfg-abcdefgh"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	chain := func() []string {
		p, err := provider.Find("fbcfg-a")
		if err != nil {
			t.Fatal(err)
		}
		return p.Fallback
	}

	// set a chain, with a duplicate and a blank that must be dropped
	if err := providerCmd([]string{"provider", "fallback", "fbcfg-a", "fbcfg-b/m2", "fbcfg-b/m2", "fbcfg-b/m3"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"fbcfg-b/m2", "fbcfg-b/m3"}; !reflect.DeepEqual(chain(), want) {
		t.Fatalf("chain = %q, want %q", chain(), want)
	}

	// an unknown provider/model rejects the whole change and keeps the old chain
	if err := providerCmd([]string{"provider", "fallback", "fbcfg-a", "fbcfg-b/m9", "nowhere/x"}); err == nil {
		t.Fatal("expected error for unknown model")
	}
	if want := []string{"fbcfg-b/m2", "fbcfg-b/m3"}; !reflect.DeepEqual(chain(), want) {
		t.Fatalf("chain changed on error: %q", chain())
	}

	// a bare name without a provider is rejected too
	if err := providerCmd([]string{"provider", "fallback", "fbcfg-a", "m2"}); err == nil {
		t.Fatal("expected error for bare model name")
	}

	// view only: no error, no change
	if err := providerCmd([]string{"provider", "fallback", "fbcfg-a"}); err != nil {
		t.Fatal(err)
	}

	// none clears
	if err := providerCmd([]string{"provider", "fallback", "fbcfg-a", "none"}); err != nil {
		t.Fatal(err)
	}
	if len(chain()) != 0 {
		t.Fatalf("chain after none = %q", chain())
	}

	// missing or unknown id
	if err := providerCmd([]string{"provider", "fallback"}); err == nil {
		t.Fatal("expected usage error without id")
	}
	if err := providerCmd([]string{"provider", "fallback", "fbcfg-nope", "fbcfg-b/m2"}); err == nil {
		t.Fatal("expected error for unknown id")
	}
}
