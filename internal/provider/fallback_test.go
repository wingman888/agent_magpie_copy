package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFallbackNormalize(t *testing.T) {
	got := normalize(Provider{ID: "a", Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m", "   "}}).Fallback
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize: got %q, want %q", got, want)
	}
	if got := normalize(Provider{ID: "a"}).Fallback; got != nil {
		t.Fatalf("empty fallback should stay nil, got %q", got)
	}
}

func TestFallbackJSONOmittedWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Provider{ID: "a", Chat: "https://x/v1", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "fallback") {
		t.Fatalf("empty fallback leaked into JSON: %s", b)
	}
}

func TestFallbackRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	if err := Save(Provider{
		ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "sk-test",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"},
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"fallback": [`) {
		t.Fatalf("stored file has no fallback key: %s", b)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"codex/gpt-5.5", "other/m"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("read back %q, want %q", p.Fallback, want)
	}

	// saving again without a fallback clears it (the field is replaced whole)
	p.Fallback = nil
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); len(p.Fallback) != 0 {
		t.Fatalf("fallback not cleared: %q", p.Fallback)
	}
}

func TestFallbackKeptForSignedInAccount(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: []string{" relay/m1 ", "relay/m1", "", "relay/m2"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), `"chat"`) {
		t.Fatalf("account stored with a URL: %s", b)
	}
	codex, ok := find(All(), "codex")
	if !ok || codex.Account == nil {
		t.Fatalf("codex not listed as an account: %+v", codex)
	}
	if want := []string{"relay/m1", "relay/m2"}; !reflect.DeepEqual(codex.Fallback, want) {
		t.Fatalf("account fallback %q, want %q", codex.Fallback, want)
	}
	if len(codex.Models) != 1 || codex.Models[0] != "gpt-5.5" {
		t.Fatalf("account models lost: %q", codex.Models)
	}
	// a later save of the account keeps its fallback unless it is given a new one
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: []string{"relay/m2"}}); err != nil {
		t.Fatal(err)
	}
	if codex, _ = find(All(), "codex"); !reflect.DeepEqual(codex.Fallback, []string{"relay/m2"}) {
		t.Fatalf("after resave: %q", codex.Fallback)
	}
}
