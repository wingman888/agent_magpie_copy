package provider

import (
	"reflect"
	"testing"
)

func TestFallbackIsCleaned(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := Save(Provider{ID: "p", Name: "P", Chat: "https://x/v1", Key: "k",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find("p")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"codex/gpt-5.5", "other/m"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
}

func TestAccountKeepsFallbackAndPicks(t *testing.T) {
	signIn(t)
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Fallback: []string{" copilot/x ", "copilot/x", "p/m"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"copilot/x", "p/m"}
	p, err := Find("codex")
	if err != nil || p.Account == nil || !reflect.DeepEqual(p.Fallback, want) || !reflect.DeepEqual(p.Models, []string{"gpt-5.5"}) {
		t.Fatalf("find: %+v %v", p, err)
	}
	a, ok := find(All(), "codex")
	if !ok || a.Account == nil || !reflect.DeepEqual(a.Fallback, want) {
		t.Fatalf("all: %+v", a)
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}, Fallback: nil}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("codex"); len(p.Fallback) != 0 {
		t.Fatalf("cleared fallback still %q", p.Fallback)
	}
}
