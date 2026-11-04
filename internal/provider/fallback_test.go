package provider

import (
	"path/filepath"
	"reflect"
	"testing"
)

// isolateConfig points the config and cache at a temp dir.
func isolateConfig(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestNormalizeFallback(t *testing.T) {
	p := normalize(Provider{ID: "relay", Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}})
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("normalize Fallback = %q, want %q", p.Fallback, want)
	}
	if p := normalize(Provider{ID: "relay", Fallback: []string{" ", ""}}); p.Fallback != nil {
		t.Fatalf("all-empty Fallback should normalize to nil, got %q", p.Fallback)
	}
}

func TestSaveFallbackRoundTrip(t *testing.T) {
	isolateConfig(t)
	err := Save(Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("read back Fallback = %q, want %q", p.Fallback, want)
	}
}

func TestCheckFallback(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Models: []string{"m1"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := CheckFallback(" relay/m1 "); err != nil || got != "relay/m1" {
		t.Fatalf("CheckFallback(relay/m1) = %q, %v; want relay/m1, nil", got, err)
	}
	for _, bad := range []string{"nowhere/x", "relay/", "relay/unknown-model", "no-slash"} {
		if got, err := CheckFallback(bad); err == nil {
			t.Errorf("CheckFallback(%q) = %q, nil; want an error", bad, got)
		}
	}
}

func TestAccountKeepsFallback(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: []string{"relay/m1", "relay/m1"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil {
		t.Fatal("codex should still be a signed-in account")
	}
	if want := []string{"relay/m1"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("account Fallback = %q, want %q", p.Fallback, want)
	}
}
