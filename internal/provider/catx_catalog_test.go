package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func catxWriteCatalog(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	dir := filepath.Join(home, "magpie")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func TestCatxCatalogIDsParsing(t *testing.T) {
	got := CatalogIDs(" OpenAI, deepseek ,,openai,")
	if len(got) != 2 || got[0] != "openai" || got[1] != "deepseek" {
		t.Fatalf("CatalogIDs: %q", got)
	}
	if got := CatalogIDs(""); len(got) != 0 {
		t.Fatalf("empty: %q", got)
	}
}

func TestCatxExposedResolvesPrefixedAndBareIDsAcrossCatalogs(t *testing.T) {
	catxWriteCatalog(t, `{
	  "openai": {"models": {
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}
	  }},
	  "deepseek": {"models": {
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"}
	  }}
	}`)
	p := Provider{ID: "catx-gw", Catalog: "openai,deepseek",
		Models: []string{"deepseek/deepseek-chat", "gpt-5.5", "mystery/model-x"}}

	avail := p.Available()
	if len(avail) != 2 {
		t.Fatalf("available across catalogs: %+v", avail)
	}
	got := p.Exposed()
	if len(got) != 3 {
		t.Fatalf("exposed: %+v", got)
	}
	if got[0].Name != "DeepSeek Chat" {
		t.Errorf("deepseek/deepseek-chat name: %+v", got[0])
	}
	if got[1].Name != "GPT-5.5" || len(got[1].Efforts) != 2 {
		t.Errorf("gpt-5.5 name/efforts: %+v", got[1])
	}
	if len(got[2].Efforts) != 0 {
		t.Errorf("unmatched model gained efforts: %+v", got[2])
	}
}
