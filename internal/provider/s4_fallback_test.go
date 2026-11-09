package provider

import (
	"reflect"
	"testing"
)

// s4ProvEnv keeps config, cache and home out of the real ones.
func s4ProvEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestS4FallbackListIsCleanedOnSave(t *testing.T) {
	s4ProvEnv(t)
	if err := Save(Provider{ID: "s4a", Name: "S4 A", Chat: "https://a.example/v1", Key: "sk-s4",
		Fallback: []string{"  b/m1 ", "", "b/m1", "c/m2", "   "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("s4a")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b/m1", "c/m2"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
	if err := Save(Provider{ID: "s4a", Name: "S4 A", Chat: "https://a.example/v1", Key: "sk-s4"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("s4a"); len(p.Fallback) != 0 {
		t.Fatalf("a save without a list left fallback %q", p.Fallback)
	}
}
