package provider

import (
	"slices"
	"testing"
)

// fbcfgIsolate points config and cache at a fresh directory, so nothing the
// test saves reaches the user's real providers.json.
func fbcfgIsolate(t *testing.T) {
	t.Helper()
	isolate(t)
}

func TestFbcfgFallbackTrimsAndDedupes(t *testing.T) {
	fbcfgIsolate(t)
	if err := Save(Provider{ID: "fbcfg-a", Name: "A", Chat: "http://127.0.0.1:9/v1", Key: "k",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m", "   "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbcfg-a")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	if !slices.Equal(p.Fallback, want) {
		t.Fatalf("Fallback = %q, want %q", p.Fallback, want)
	}
}

func TestFbcfgFallbackEmptyStaysEmpty(t *testing.T) {
	fbcfgIsolate(t)
	if err := Save(Provider{ID: "fbcfg-b", Name: "B", Chat: "http://127.0.0.1:9/v1", Key: "k",
		Fallback: []string{"", "  "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbcfg-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Fallback) != 0 {
		t.Fatalf("Fallback = %q, want empty", p.Fallback)
	}
}

func TestFbcfgFallbackKeptForSignedInAccount(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: []string{" other/m ", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	// reading back goes through the file again, as after a restart
	p, err := Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if p.Account == nil {
		t.Fatal("codex should still be a signed-in account")
	}
	if !slices.Equal(p.Fallback, []string{"other/m"}) {
		t.Fatalf("Fallback = %q, want [other/m]", p.Fallback)
	}
	if !slices.Equal(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("Models = %q, want [gpt-5.5]", p.Models)
	}
}
