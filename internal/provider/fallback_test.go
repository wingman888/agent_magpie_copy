package provider

import (
	"path/filepath"
	"reflect"
	"testing"
)

// fallbackHome points every config and cache path at a temp dir.
func fallbackHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestFallbackNormalizedOnSaveAndRead(t *testing.T) {
	fallbackHome(t)
	p := Provider{
		ID:       "a",
		Name:     "A",
		Chat:     "https://a.example.com/v1",
		Key:      "sk-test",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"},
	}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	got, err := Find("a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(got.Fallback, want) {
		t.Fatalf("Fallback = %q, want %q", got.Fallback, want)
	}
}

func TestFallbackOmittedWhenEmpty(t *testing.T) {
	fallbackHome(t)
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example.com/v1", Key: "k", Fallback: []string{" ", ""}}); err != nil {
		t.Fatal(err)
	}
	got, err := Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Fallback != nil {
		t.Fatalf("Fallback = %q, want nil", got.Fallback)
	}
}

func TestFallbackKeptForSignedInAccount(t *testing.T) {
	signIn(t)
	accts := Accounts()
	if len(accts) == 0 {
		t.Fatal("expected signed-in accounts from signIn")
	}
	id := accts[0].ID
	if err := Save(Provider{ID: id, Fallback: []string{"a/m1", "a/m1", " b/m2 "}}); err != nil {
		t.Fatal(err)
	}
	// Read it back the way a restart would: from the file alone.
	got, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a/m1", "b/m2"}
	if !reflect.DeepEqual(got.Fallback, want) {
		t.Fatalf("Fallback = %q, want %q", got.Fallback, want)
	}
	if got.Account == nil {
		t.Fatal("account lost its sign-in")
	}
}
