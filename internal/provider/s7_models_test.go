package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A gateway that takes several catalogs and lists its models with a vendor
// prefix still gets names and reasoning levels, with no live list fetched.
func TestS7MultiCatalogPrefixedIDsGetDetails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	body := `{
	  "openai": {"models": {
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5",
	                "reasoning_options":[{"type":"effort","values":["low","high"]}]}
	  }},
	  "deepseek": {"models": {
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat"},
	    "deepseek-reasoner": {"id":"deepseek-reasoner","name":"DeepSeek Reasoner",
	                "reasoning_options":[{"type":"effort","values":["high"]}]}
	  }}
	}`
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	p := Provider{ID: "gw", Catalog: "openai,deepseek", Models: []string{"deepseek/deepseek-reasoner", "openai/gpt-5.5", "gpt-5.5", "unknown/x"}}
	got := map[string]catalog.Model{}
	for _, m := range p.Exposed() {
		got[m.ID] = m
	}

	ds := got["deepseek/deepseek-reasoner"]
	if ds.Name != "DeepSeek Reasoner" || len(ds.Efforts) != 1 || ds.Efforts[0] != "high" {
		t.Fatalf("prefixed deepseek model: %+v", ds)
	}
	gpt := got["openai/gpt-5.5"]
	if gpt.Name != "GPT-5.5" || len(gpt.Efforts) != 2 || gpt.Efforts[0] != "low" {
		t.Fatalf("prefixed gpt model: %+v", gpt)
	}
	if bare := got["gpt-5.5"]; bare.Name != "GPT-5.5" || len(bare.Efforts) != 2 {
		t.Fatalf("bare gpt model: %+v", bare)
	}
	// an id nothing knows is returned bare, without details
	if u := got["unknown/x"]; u.Name != "unknown/x" || len(u.Efforts) != 0 {
		t.Fatalf("unknown model: %+v", u)
	}
}
