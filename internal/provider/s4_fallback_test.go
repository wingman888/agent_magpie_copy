package provider

import (
	"reflect"
	"testing"
)

// magS4ProvEnv keeps config, cache and home out of the real ones.
func magS4ProvEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func TestS4FallbackListIsCleanedOnSave(t *testing.T) {
	magS4ProvEnv(t)
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
	// a save that does not give a list keeps the saved one; an empty list clears it
	if err := Save(Provider{ID: "s4a", Name: "S4 A", Chat: "https://a.example/v1", Key: "sk-s4"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("s4a"); !reflect.DeepEqual(p.Fallback, []string{"b/m1", "c/m2"}) {
		t.Fatalf("a save without a list changed fallback to %q", p.Fallback)
	}
	if err := Save(Provider{ID: "s4a", Name: "S4 A", Chat: "https://a.example/v1", Key: "sk-s4", Fallback: []string{}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("s4a"); len(p.Fallback) != 0 {
		t.Fatalf("an empty list left fallback %q", p.Fallback)
	}
}
