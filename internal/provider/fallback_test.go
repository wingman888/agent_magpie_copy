package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestFallbackListIsCleanedAndDeduplicated(t *testing.T) {
	keysHome(t)
	saveRelay(t, Provider{ID: "relay", Name: "Relay", Chat: "https://r.example/v1", Key: "sk-r",
		Fallback: []string{"  a/b ", "a/b", "", "   ", "c/d"}})
	if got := fallbackOf(t, "relay"); strings.Join(got, "|") != "a/b|c/d" {
		t.Fatalf("fallback: %q", got)
	}
	if err := SetFallback("relay", []string{"", "x/y", " x/y", "z/w"}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "relay"); strings.Join(got, "|") != "x/y|z/w" {
		t.Fatalf("after SetFallback: %q", got)
	}
	if err := SetFallback("relay", []string{""}); err != nil {
		t.Fatal(err)
	}
	if got := fallbackOf(t, "relay"); len(got) != 0 {
		t.Fatalf("an empty list should clear the fallbacks: %q", got)
	}
	if err := SetFallback("nope", []string{"a/b"}); err == nil {
		t.Fatal("fallbacks set on a provider that does not exist")
	}
}

func TestFallbacksSkipUnusableEntries(t *testing.T) {
	keysHome(t)
	saveRelay(t, Provider{ID: "relay", Name: "Relay", Chat: "https://r.example/v1", Key: "sk-r"})
	got := Fallbacks([]string{"relay", "nope/m1", "/m1", "relay/", "ghost/m3", "relay/m2"})
	if len(got) != 1 || got[0].Provider.ID != "relay" || got[0].Model != "m2" {
		t.Fatalf("resolved: %+v", got)
	}
}

func TestParseFallbackSplitsAtFirstSlash(t *testing.T) {
	id, model, ok := ParseFallback(" deepseek/deepseek-chat ")
	if !ok || id != "deepseek" || model != "deepseek-chat" {
		t.Fatalf("parsed %q %q %v", id, model, ok)
	}
	if _, _, ok := ParseFallback("deepseek"); ok {
		t.Error("an entry with no model parsed")
	}
}

func TestCatalogIsAnOrderedList(t *testing.T) {
	keysHome(t)
	saveRelay(t, Provider{ID: "relay", Name: "Relay", Chat: "https://r.example/v1", Key: "sk-r",
		Catalog: " OpenAI, deepseek,,openai "})
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog: %q", p.Catalog)
	}
	if got := strings.Join(CatalogNames("deepseek,openai"), "|"); got != "deepseek|openai" {
		t.Fatalf("CatalogNames: %q", got)
	}
}

func TestCatalogsSearchInOrderAndMatchPrefixes(t *testing.T) {
	keysHome(t)
	writeModels(t, `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]},
	    "shared": {"id":"shared","name":"OpenAI shared"}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"},
	    "shared": {"id":"shared","name":"DeepSeek shared"}}}
	}`)
	saveRelay(t, Provider{ID: "gw", Name: "Gateway", Chat: "https://gw.example/v1", Key: "sk-g",
		Catalog: "openai,deepseek", Models: []string{"deepseek/deepseek-chat", "gpt-5.5", "shared", "unknown-x"}})
	p, err := Find("gw")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]catalog.Model{}
	for _, m := range p.Exposed() {
		byID[m.ID] = m
	}
	if m := byID["deepseek/deepseek-chat"]; m.Name != "DeepSeek Chat" {
		t.Errorf("prefixed id: %+v", m)
	}
	if m := byID["gpt-5.5"]; m.Name != "GPT-5.5" || strings.Join(m.Efforts, "/") != "low/high" {
		t.Errorf("gpt-5.5: %+v", m)
	}
	if m := byID["shared"]; m.Name != "OpenAI shared" {
		t.Errorf("a model two catalogs list should come from the first named: %+v", m)
	}
	if m := byID["unknown-x"]; m.Name != "unknown-x" || len(m.Efforts) != 0 {
		t.Errorf("unmatched model: %+v", m)
	}
}

// saveRelay saves a custom provider, failing the test on error.
func saveRelay(t *testing.T, p Provider) {
	t.Helper()
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
}

// fallbackOf reads a provider's fallback list back from the file.
func fallbackOf(t *testing.T, id string) []string {
	t.Helper()
	p, err := Find(id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Fallback
}

// writeModels puts a models.dev-shaped catalog where the catalog package
// will find it, under the test's own cache directory.
func writeModels(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}
