package provider

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// The fallback list is cleaned on the way in: blanks and repeats go, and the
// order the user gave is kept.
func TestFbchainFallbackListIsCleanedAndStored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := Save(Provider{ID: "fbc", Name: "FBC", Chat: "https://x.example.com/v1", Key: "sk-fbc-1",
		Fallback: []string{" a/b ", "", "a/b", "c/d/e", "   "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbc")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a/b", "c/d/e"}; !slices.Equal(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
	if err := SetFallback("fbc", nil); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("fbc"); len(p.Fallback) != 0 {
		t.Fatalf("clearing left %q", p.Fallback)
	}
}

// A catalog field may name several models.dev catalogs; the list is kept in
// the order named, without blanks or repeats.
func TestFbchainCatalogFieldIsAList(t *testing.T) {
	want := []string{"openai", "deepseek"}
	for _, in := range []string{"openai,deepseek", " OpenAI , deepseek,,openai ", "openai, deepseek"} {
		if got := CatalogIDs(in); !slices.Equal(got, want) {
			t.Errorf("CatalogIDs(%q) = %q, want %q", in, got, want)
		}
	}
	if got := CatalogIDs(""); len(got) != 0 {
		t.Errorf("empty catalog gave %q", got)
	}
}

// With catalog=openai,deepseek a prefixed live id ("deepseek/deepseek-chat")
// and a bare one ("gpt-5.5") both get their names and reasoning levels.
func TestFbchainAvailableReadsEveryCatalog(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", cache)
	if err := os.MkdirAll(filepath.Join(cache, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
	  "openai": {"models": {"gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5",
	    "reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"models": {"deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	if err := catalog.SaveLive("fbc", "https://x.example.com/v1", []catalog.Model{
		{ID: "deepseek/deepseek-chat", Name: "deepseek/deepseek-chat"},
		{ID: "gpt-5.5", Name: "gpt-5.5"},
		{ID: "unlisted/thing", Name: "unlisted/thing"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "fbc", Name: "FBC", Chat: "https://x.example.com/v1", Key: "sk-fbc-2", Catalog: " OpenAI, deepseek "}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbc")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog stored as %q", p.Catalog)
	}
	byID := map[string]catalog.Model{}
	for _, m := range p.Available() {
		byID[m.ID] = m
	}
	if m := byID["deepseek/deepseek-chat"]; m.Name != "DeepSeek Chat" {
		t.Fatalf("deepseek/deepseek-chat: %+v", m)
	}
	if m := byID["gpt-5.5"]; m.Name != "GPT-5.5" || len(m.Efforts) != 2 {
		t.Fatalf("gpt-5.5: %+v", m)
	}
	if m := byID["unlisted/thing"]; m.Name != "unlisted/thing" || len(m.Efforts) != 0 {
		t.Fatalf("an unmatched id must come back without name or levels: %+v", m)
	}
}
