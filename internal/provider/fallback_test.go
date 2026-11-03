package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fallbackHome points every config, cache and login lookup at a temp home.
func fallbackHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
}

func TestFallbackNormalizedOnSaveAndLoad(t *testing.T) {
	fallbackHome(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "k",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("Fallback = %q, want %q", p.Fallback, want)
	}
	// it is on disk under the "fallback" key
	b, _ := os.ReadFile(Path())
	var f struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if got, _ := json.Marshal(f.Providers[0]["fallback"]); string(got) != `["codex/gpt-5.5","other/m"]` {
		t.Fatalf("stored fallback = %s", got)
	}
}

func TestFallbackOmittedWhenEmpty(t *testing.T) {
	fallbackHome(t)
	if err := Save(Provider{ID: "plain", Chat: "https://plain.example/v1", Key: "k", Fallback: []string{" ", ""}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "fallback") {
		t.Fatalf("empty fallback should not be stored: %s", b)
	}
}

func TestFallbackKeptForSignedInAccount(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: []string{"other/m", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	// reading the file again is what a restart does
	codex, ok := find(All(), "codex")
	if !ok {
		t.Fatal("codex not listed")
	}
	if !reflect.DeepEqual(codex.Fallback, []string{"other/m"}) {
		t.Fatalf("Fallback = %q", codex.Fallback)
	}
	if len(codex.Exposed()) != 1 || codex.Exposed()[0].ID != "gpt-5.5" {
		t.Fatalf("picks lost alongside fallback: %+v", codex.Exposed())
	}

	// changing the picks keeps the fallback, and changing the fallback keeps the picks
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if codex, _ = find(All(), "codex"); len(codex.Fallback) != 0 {
		t.Fatalf("saving without a fallback should clear it, got %q", codex.Fallback)
	}
	if _, err := SetFallback("codex", []string{"codex/gpt-5.5"}); err != nil {
		t.Fatal(err)
	}
	if codex, _ = find(All(), "codex"); !reflect.DeepEqual(codex.Fallback, []string{"codex/gpt-5.5"}) || len(codex.Exposed()) != 1 {
		t.Fatalf("after SetFallback: fallback %q picks %+v", codex.Fallback, codex.Exposed())
	}
}

func TestSetFallbackValidatesBeforeSaving(t *testing.T) {
	fallbackHome(t)
	must := func(p Provider) {
		t.Helper()
		if err := Save(p); err != nil {
			t.Fatal(err)
		}
	}
	must(Provider{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k", Models: []string{"m1", "m3"}})
	must(Provider{ID: "b", Name: "B", Chat: "https://b.example/v1", Key: "k", Models: []string{"m2"}})

	if p, err := SetFallback("a", []string{"b/m2"}); err != nil || !reflect.DeepEqual(p.Fallback, []string{"b/m2"}) {
		t.Fatalf("set: %+v %v", p, err)
	}
	// one bad name fails the whole call and nothing changes
	for _, bad := range [][]string{{"b/m2", "nowhere/x"}, {"b/nope"}, {"b"}, {"/m2"}, {"b/"}} {
		if _, err := SetFallback("a", bad); err == nil {
			t.Fatalf("SetFallback(%q) accepted", bad)
		}
		if p, _ := Find("a"); !reflect.DeepEqual(p.Fallback, []string{"b/m2"}) {
			t.Fatalf("after failed %q: fallback %q", bad, p.Fallback)
		}
	}
	if _, err := SetFallback("nope", []string{"b/m2"}); err == nil {
		t.Fatal("unknown provider accepted")
	}

	// order is kept, repeats and blanks dropped, and the replacement is total
	if p, err := SetFallback("a", []string{" b/m2 ", "a/m3", "b/m2", ""}); err != nil || !reflect.DeepEqual(p.Fallback, []string{"b/m2", "a/m3"}) {
		t.Fatalf("reorder: %+v %v", p, err)
	}
	if p, err := SetFallback("a", nil); err != nil || len(p.Fallback) != 0 {
		t.Fatalf("clear: %+v %v", p, err)
	}
	if p, _ := Find("a"); len(p.Fallback) != 0 || len(p.Models) != 2 {
		t.Fatalf("clear touched the rest: %+v", p)
	}
}
