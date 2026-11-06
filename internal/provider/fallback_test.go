package provider

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// useHome points magpie's config and cache at a fresh temp home.
func useHome(t *testing.T) {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home+"/.config")
	t.Setenv("XDG_CACHE_HOME", home+"/.cache")
}

func TestFallbackNormalizedAndSaved(t *testing.T) {
	useHome(t)
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	p, err := Find("a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("read back %q, want %q", p.Fallback, want)
	}
	b, _ := os.ReadFile(Path())
	if !strings.Contains(string(b), `"fallback"`) {
		t.Fatalf("stored file lacks fallback key: %s", b)
	}

	// a provider with no fallback keeps the key out of the file
	if err := Save(Provider{ID: "plain", Chat: "https://p.example/v1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(Path())
	if strings.Count(string(b), `"fallback"`) != 1 {
		t.Fatalf("empty fallback should be omitted: %s", b)
	}
}

func TestFallbackKeptForSignedInAccount(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: []string{" a/m ", "a/m", "b/n"}}); err != nil {
		t.Fatal(err)
	}
	// a fresh read, as after a restart
	codex, ok := find(All(), "codex")
	if !ok || codex.Account == nil {
		t.Fatalf("codex not listed: %+v", codex)
	}
	if want := []string{"a/m", "b/n"}; !reflect.DeepEqual(codex.Fallback, want) {
		t.Fatalf("fallback %q, want %q", codex.Fallback, want)
	}
	if len(codex.Models) != 1 || codex.Models[0] != "gpt-5.5" {
		t.Fatalf("picks lost: %q", codex.Models)
	}
}

func TestSplitRef(t *testing.T) {
	for _, ref := range []string{"nomodel", "/m", "a/", " / "} {
		if _, _, ok := SplitRef(ref); ok {
			t.Errorf("SplitRef(%q) accepted", ref)
		}
	}
	for ref, want := range map[string][2]string{
		"a/m":             {"a", "m"},
		" a / m ":         {"a", "m"},
		"or/vendor/model": {"or", "vendor/model"},
	} {
		pid, model, ok := SplitRef(ref)
		if !ok || pid != want[0] || model != want[1] {
			t.Errorf("SplitRef(%q) = %q, %q, %v; want %q, %q", ref, pid, model, ok, want[0], want[1])
		}
	}
}

func TestKnown(t *testing.T) {
	useHome(t)
	if err := Save(Provider{ID: "a", Name: "A", Chat: "https://a.example/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	for ref, want := range map[string]bool{
		"a/m1":      true,
		"a/nope":    false,
		"nowhere/x": false,
		"a":         false,
	} {
		if got := Known(ref); got != want {
			t.Errorf("Known(%q) = %v, want %v", ref, got, want)
		}
	}
}
