package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A gateway that resells several vendors names each catalog; its models
// are looked up in them in order (#17).
func TestSeveralCatalogs(t *testing.T) {
	isolate(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	os.MkdirAll(filepath.Join(cache, "magpie"), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{
	  "openai": {"models": {"gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"models": {"deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek V4","reasoning_options":[{"type":"effort","values":["high","max"]}]}}}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	if err := Save(Provider{ID: "gw", Name: "Gateway", Key: "k", Chat: "https://gw.example/v1", Catalog: "OpenAI,, deepseek openai"}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("gw")
	if err != nil || p.Catalog != "openai, deepseek" {
		t.Fatalf("catalog %q %v", p.Catalog, err)
	}
	names := map[string]string{}
	for _, m := range p.Available() {
		names[m.ID] = m.Name
	}
	if names["gpt-5.5"] != "GPT-5.5" || names["deepseek-chat"] != "DeepSeek V4" {
		t.Fatalf("available %v", names)
	}
	// the vendor's own list, ids prefixed the gateway's way
	live := catalog.Decorate([]catalog.Model{{ID: "deepseek/deepseek-chat"}, {ID: "internal-x"}}, p.Available())
	if live[0].Name != "DeepSeek V4" || len(live[0].Efforts) != 2 || live[1].Efforts != nil {
		t.Fatalf("decorated %+v", live)
	}
}
