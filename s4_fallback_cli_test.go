package main

import (
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestS4ProviderFallbackCommand(t *testing.T) {
	s1CLIEnv(t)
	if err := provider.Save(provider.Provider{ID: "s4cli", Name: "S4 CLI", Chat: "https://a.example/v1", Key: "sk-s4"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"magpie", "provider", "fallback", "s4cli", " x/one ", "x/one", "", "y/two"}); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("s4cli")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"x/one", "y/two"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
	if err := providerCmd([]string{"magpie", "provider", "fallback", "s4cli"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"magpie", "provider", "fallback", "s4cli", ""}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("s4cli"); len(p.Fallback) != 0 {
		t.Fatalf("clearing left fallback %q", p.Fallback)
	}
}
