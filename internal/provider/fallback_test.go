package provider

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// fallbackHome points magpie's config at a fresh temp home with no logins.
func fallbackHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
}

func TestFallbackNormalizedOnSaveAndRead(t *testing.T) {
	fallbackHome(t)

	in := []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m", "   "}
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Fallback: in}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"fallback"`) {
		t.Fatalf("fallback key missing from stored file: %s", b)
	}

	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("read back fallback = %q, want %q", p.Fallback, want)
	}
}

func TestFallbackNormalizedWhenStoredByHand(t *testing.T) {
	fallbackHome(t)

	// a file edited by hand, with dirty entries, still reads back clean
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	edited := strings.Replace(string(b), `"name": "Relay"`, `"name": "Relay", "fallback": [" a/x ", "", "a/x", "b/y"]`, 1)
	if err := os.WriteFile(Path(), []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/x", "b/y"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("read back fallback = %q, want %q", p.Fallback, want)
	}
}

func TestFallbackEmptyStaysOutOfFile(t *testing.T) {
	fallbackHome(t)

	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "k", Fallback: []string{" ", ""}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), `"fallback"`) {
		t.Fatalf("empty fallback should be omitted: %s", b)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fallback) != 0 {
		t.Fatalf("fallback = %q, want empty", p.Fallback)
	}
}

func TestFallbackKeptForSignedInAccount(t *testing.T) {
	signIn(t)

	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: []string{" copilot/gpt-5.5 ", "copilot/gpt-5.5", "other/m"}}); err != nil {
		t.Fatal(err)
	}

	b, _ := os.ReadFile(Path())
	if !strings.Contains(string(b), `"fallback"`) || strings.Contains(string(b), "auth_mode") {
		t.Fatalf("stored account entry: %s", b)
	}

	// reading again from disk, as after a restart
	codex, ok := find(All(), "codex")
	if !ok || codex.Account == nil {
		t.Fatalf("codex not listed as an account: %+v", codex)
	}
	want := []string{"copilot/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(codex.Fallback, want) {
		t.Fatalf("account fallback = %q, want %q", codex.Fallback, want)
	}
	if len(codex.Models) != 1 || codex.Models[0] != "gpt-5.5" {
		t.Fatalf("account models = %q", codex.Models)
	}

	// saving again without a fallback clears it
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	codex, _ = find(All(), "codex")
	if len(codex.Fallback) != 0 {
		t.Fatalf("fallback after clear = %q", codex.Fallback)
	}
}
