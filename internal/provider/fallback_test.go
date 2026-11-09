package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// fbcIsolate points magpie's config, cache and home at temp dirs.
func fbcIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

// fbcCatalog writes a two-vendor models.dev catalog where catalog reads it.
func fbcCatalog(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "openai": {"id": "openai", "name": "OpenAI", "models": {
    "gpt-5.5": {"id": "gpt-5.5", "name": "GPT-5.5", "release_date": "2026-03-01",
      "reasoning_options": [{"type": "effort", "values": ["low", "high"]}], "modalities": {"output": ["text"]}}
  }},
  "deepseek": {"id": "deepseek", "name": "DeepSeek", "models": {
    "deepseek-chat": {"id": "deepseek-chat", "name": "DeepSeek Chat", "release_date": "2025-06-01",
      "modalities": {"output": ["text"]}}
  }}
}`
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func TestFbcCatalogsMergedAndPrefixedIDsNamed(t *testing.T) {
	fbcIsolate(t)
	fbcCatalog(t)
	if err := Save(Provider{ID: "fbc-gw", Name: "fbc gw", Chat: "https://gw.example.com/v1", Key: "k",
		Catalog: " OpenAI, deepseek ,,", Models: []string{"deepseek/deepseek-chat", "gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbc-gw")
	if err != nil {
		t.Fatal(err)
	}
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("catalog = %q, want the two ids in order", p.Catalog)
	}
	got := map[string]catalog.Model{}
	for _, m := range p.Exposed() {
		got[m.ID] = m
	}
	ds, ok := got["deepseek/deepseek-chat"]
	if !ok {
		t.Fatalf("deepseek/deepseek-chat not exposed: %v", got)
	}
	if ds.Name != "DeepSeek Chat" {
		t.Fatalf("name = %q, want the catalog's name", ds.Name)
	}
	gpt, ok := got["gpt-5.5"]
	if !ok {
		t.Fatalf("gpt-5.5 not exposed")
	}
	if gpt.Name != "GPT-5.5" || strings.Join(gpt.Efforts, "/") != "low/high" {
		t.Fatalf("gpt-5.5 = name %q efforts %v, want GPT-5.5 low/high", gpt.Name, gpt.Efforts)
	}
}

func TestFbcUnknownPrefixedModelKeptAsIs(t *testing.T) {
	fbcIsolate(t)
	fbcCatalog(t)
	if err := Save(Provider{ID: "fbc-gw", Name: "fbc gw", Chat: "https://gw.example.com/v1", Key: "k",
		Catalog: "deepseek", Models: []string{"nobody/unknown-model"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbc-gw")
	if err != nil {
		t.Fatal(err)
	}
	ms := p.Exposed()
	if len(ms) != 1 || ms[0].ID != "nobody/unknown-model" || ms[0].Name != "nobody/unknown-model" || len(ms[0].Efforts) != 0 {
		t.Fatalf("exposed = %+v, want the id as is with no name or efforts", ms)
	}
}

func TestFbcFallbackListCleaned(t *testing.T) {
	fbcIsolate(t)
	if err := Save(Provider{ID: "fbc-a", Name: "fbc a", Chat: "https://a.example.com/v1", Key: "k",
		Fallback: []string{" x/y ", "", "x/y", "z/w"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("fbc-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Fallback, "|") != "x/y|z/w" {
		t.Fatalf("fallback = %q, want x/y then z/w", p.Fallback)
	}
	if err := SetFallback("fbc-a", []string{"  ", "q/r", "q/r"}); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("fbc-a")
	if strings.Join(p.Fallback, "|") != "q/r" {
		t.Fatalf("fallback after set = %q", p.Fallback)
	}
	if p.Key != "k" {
		t.Fatalf("setting the fallback changed the key to %q", p.Key)
	}
	if err := SetFallback("fbc-a", nil); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("fbc-a")
	if len(p.Fallback) != 0 {
		t.Fatalf("fallback not cleared: %q", p.Fallback)
	}
}
