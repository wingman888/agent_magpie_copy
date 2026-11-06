package provider

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A relay's "vendor/model" pick takes the name and reasoning levels of the
// model after the slash, even before the vendor's own list has been fetched,
// and the id stays as written so the vendor is sent it unchanged.
func TestPrefixedPickGetsNameAndEffortsFromCatalog(t *testing.T) {
	isolateConfig(t)
	cache := t.TempDir()
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

	p := Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example/v1", Key: "sk-test",
		Catalog: "openai,deepseek", Models: []string{"deepseek/deepseek-chat", "gpt-5.5"}}
	ms := p.Exposed()
	if len(ms) != 2 {
		t.Fatalf("exposed %+v, want 2", ms)
	}
	if ms[0].ID != "deepseek/deepseek-chat" || ms[0].Name != "DeepSeek Chat" {
		t.Fatalf("prefixed pick = %+v, want its id kept and the catalog name", ms[0])
	}
	if ms[1].ID != "gpt-5.5" || ms[1].Name != "GPT-5.5" || len(ms[1].Efforts) != 2 {
		t.Fatalf("plain pick = %+v, want name and efforts", ms[1])
	}
}
