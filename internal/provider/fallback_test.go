package provider

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestFallbackNormalizedOnSave(t *testing.T) {
	signIn(t)
	in := Provider{ID: "relay", Chat: "https://relay.example.com/v1", Key: "sk-x",
		Fallback: []string{" codex/gpt-5.5 ", "", "codex/gpt-5.5", "other/m"}}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	want := []string{"codex/gpt-5.5", "other/m"}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback %q, want %q", p.Fallback, want)
	}
	// on disk too, under the "fallback" key
	var raw struct {
		Providers []map[string]any `json:"providers"`
	}
	b, _ := os.ReadFile(Path())
	if err := json.Unmarshal(b, &raw); err != nil || len(raw.Providers) != 1 {
		t.Fatalf("providers.json: %s (%v)", b, err)
	}
	if got, _ := json.Marshal(raw.Providers[0]["fallback"]); string(got) != `["codex/gpt-5.5","other/m"]` {
		t.Fatalf("stored fallback %s", got)
	}

	// clearing drops the key entirely
	p.Fallback = nil
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); len(p.Fallback) != 0 {
		t.Fatalf("fallback not cleared: %q", p.Fallback)
	}
	if b, _ := os.ReadFile(Path()); strings.Contains(string(b), "fallback") {
		t.Fatalf("empty fallback stored: %s", b)
	}
}

func TestFallbackNormalizedOnLoad(t *testing.T) {
	signIn(t)
	writeFile(t, Path(), map[string]any{"providers": []any{map[string]any{
		"id": "relay", "chat": "https://relay.example.com/v1", "key": "sk-x",
		"fallback": []string{"", " a/b", "a/b", "c/d "},
	}}})
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/b", "c/d"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback %q, want %q", p.Fallback, want)
	}
}

func TestAccountFallbackPersists(t *testing.T) {
	signIn(t)
	codex, err := Find("codex")
	if err != nil || codex.Account == nil {
		t.Fatalf("codex: %+v %v", codex, err)
	}
	codex.Models = []string{"gpt-5.5"}
	codex.Fallback = []string{" relay/m1", "relay/m1", "", "other/m2"}
	if err := Save(*codex); err != nil {
		t.Fatal(err)
	}
	// a fresh read, as after a restart: nothing is cached in memory
	got, err := Find("codex")
	if err != nil || got.Account == nil {
		t.Fatalf("codex after save: %+v %v", got, err)
	}
	if want := []string{"relay/m1", "other/m2"}; !reflect.DeepEqual(got.Fallback, want) {
		t.Fatalf("fallback %q, want %q", got.Fallback, want)
	}
	if !reflect.DeepEqual(got.Models, []string{"gpt-5.5"}) {
		t.Fatalf("models %q", got.Models)
	}
	// the entry on disk is still only the account's picks
	var f file
	b, _ := os.ReadFile(Path())
	if err := json.Unmarshal(b, &f); err != nil || len(f.Providers) != 1 {
		t.Fatalf("providers.json: %s", b)
	}
	if s := f.Providers[0]; s.Chat != "" || s.Key != "" || !reflect.DeepEqual(s.Fallback, []string{"relay/m1", "other/m2"}) {
		t.Fatalf("stored account entry: %+v", s)
	}

	// changing only the model picks keeps the fallbacks
	got.Models = nil
	if err := Save(*got); err != nil {
		t.Fatal(err)
	}
	if again, _ := Find("codex"); len(again.Fallback) != 2 {
		t.Fatalf("fallback lost on a models save: %q", again.Fallback)
	}

	// hiding and bringing the account back keeps them as well
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Fallback: []string{"relay/m1"}}); err != nil {
		t.Fatal(err)
	}
	if back, _ := Find("codex"); !reflect.DeepEqual(back.Fallback, []string{"relay/m1"}) {
		t.Fatalf("fallback after re-adding: %q", back.Fallback)
	}
}
