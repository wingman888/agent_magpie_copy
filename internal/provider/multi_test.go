package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

func TestFallbackIsCleanedAndSaved(t *testing.T) {
	isolateConfig(t)
	if err := Save(Provider{ID: "p1", Name: "P1", Chat: "https://p1.example/v1", Key: "k",
		Fallback: []string{" openai/gpt-5.5 ", "", "deepseek/deepseek-chat", "openai/gpt-5.5", "   "}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("p1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"openai/gpt-5.5", "deepseek/deepseek-chat"}
	if !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q, want %q", p.Fallback, want)
	}
}

func TestCatalogListIsCleaned(t *testing.T) {
	if got := normalize(Provider{ID: "x", Catalog: " openai , ,deepseek, openai "}).Catalog; got != "openai,deepseek" {
		t.Fatalf("catalog = %q", got)
	}
}

// writeModels puts a models.dev-shaped cache where catalog.load() looks.
func writeModels(t *testing.T) {
	t.Helper()
	body := `{
	  "openai": {"id":"openai","name":"OpenAI","env":[],"models":{
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}],"modalities":{"output":["text"]}}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","env":[],"models":{
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat","modalities":{"output":["text"]}}}}
	}`
	p := catalog.CachePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

func TestMultipleCatalogsAndPrefixedIDs(t *testing.T) {
	isolateConfig(t)
	writeModels(t)
	p := Provider{ID: "gw", Name: "GW", Chat: "https://gw.example/v1", Key: "k", Catalog: "openai, deepseek"}

	names := map[string]bool{}
	for _, m := range catalogModels(p.Catalog) {
		names[m.ID] = true
	}
	if !names["gpt-5.5"] || !names["deepseek-chat"] {
		t.Fatalf("catalogs openai,deepseek should list both models, got %v", names)
	}

	// what a relay that namespaces its ids reports
	if err := catalog.SaveLive("gw", p.Chat, []catalog.Model{
		{ID: "deepseek/deepseek-chat", Name: "deepseek/deepseek-chat"},
		{ID: "gpt-5.5", Name: "gpt-5.5"},
		{ID: "vendor/unknown-x", Name: "vendor/unknown-x"},
	}); err != nil {
		t.Fatal(err)
	}
	got := map[string]catalog.Model{}
	for _, m := range p.Available() {
		got[m.ID] = m
	}
	if m := got["deepseek/deepseek-chat"]; m.Name != "DeepSeek Chat" {
		t.Errorf("deepseek/deepseek-chat name = %q, want the catalog's", m.Name)
	}
	if m := got["gpt-5.5"]; m.Name != "GPT-5.5" || !reflect.DeepEqual(m.Efforts, []string{"low", "high"}) {
		t.Errorf("gpt-5.5 = %+v, want its name and efforts", m)
	}
	if m := got["vendor/unknown-x"]; m.Name != "vendor/unknown-x" || len(m.Efforts) != 0 {
		t.Errorf("unmatched model = %+v, want it as the vendor listed it", m)
	}
}
