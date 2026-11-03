package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// useCatalog writes a models.dev-shaped cache where the catalog package will
// find it, isolated from the developer's real caches.
func useCatalog(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

const twoCatalogs = `{
  "openai":   {"models": {"gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5",
                 "reasoning_options":[{"type":"effort","values":["low","high"]}],"temperature":false}}},
  "deepseek": {"models": {"deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat","temperature":false}}}
}`

func TestSplitCatalogsTrimsAndDropsBlanks(t *testing.T) {
	got := SplitCatalogs(" OpenAI, ,deepseek,,openai ,")
	if want := []string{"openai", "deepseek"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitCatalogs = %v, want %v", got, want)
	}
	if got := SplitCatalogs("  ,  "); len(got) != 0 {
		t.Fatalf("blank list: %v", got)
	}
}

func TestNormalizeCleansCatalogList(t *testing.T) {
	p := normalize(Provider{ID: "gw", Catalog: " openai, deepseek ,"})
	if p.Catalog != "openai,deepseek" {
		t.Fatalf("normalized catalog: %q", p.Catalog)
	}
	if p.PrimaryCatalog() != "openai" {
		t.Fatalf("primary: %q", p.PrimaryCatalog())
	}
	if got := (Provider{}).PrimaryCatalog(); got != "" {
		t.Fatalf("no catalog primary: %q", got)
	}
}

func TestExposedReadsSeveralCatalogsWithPrefixedIDs(t *testing.T) {
	useCatalog(t, twoCatalogs)
	p := Provider{ID: "gw", Name: "GW", Catalog: "openai,deepseek",
		Models: []string{"deepseek/deepseek-chat", "gpt-5.5", "relay/unknown"}}

	ms := p.Exposed()
	if len(ms) != 3 {
		t.Fatalf("exposed: %+v", ms)
	}
	if ms[0].ID != "deepseek/deepseek-chat" || ms[0].Name != "DeepSeek Chat" {
		t.Errorf("prefixed model: %+v", ms[0])
	}
	if ms[1].Name != "GPT-5.5" || !reflect.DeepEqual(ms[1].Efforts, []string{"low", "high"}) {
		t.Errorf("gpt-5.5: %+v", ms[1])
	}
	if ms[2].ID != "relay/unknown" || ms[2].Name != "relay/unknown" || len(ms[2].Efforts) != 0 {
		t.Errorf("unmatched model should pass through unchanged: %+v", ms[2])
	}
}

func TestAvailableUnionsCatalogs(t *testing.T) {
	useCatalog(t, twoCatalogs)
	p := Provider{ID: "gw", Catalog: "deepseek,openai"}
	ids := map[string]bool{}
	for _, m := range p.Available() {
		ids[m.ID] = true
	}
	if !ids["gpt-5.5"] || !ids["deepseek-chat"] || len(ids) != 2 {
		t.Fatalf("available: %v", ids)
	}
}

func TestRejectsTemperatureSeesPrefix(t *testing.T) {
	useCatalog(t, twoCatalogs)
	p := Provider{ID: "gw", Catalog: "openai,deepseek"}
	if !p.RejectsTemperature("deepseek/deepseek-chat") {
		t.Error("prefixed model should reject temperature")
	}
	if p.RejectsTemperature("relay/unknown") {
		t.Error("unknown model should not reject temperature")
	}
}

func TestFetchedListDecoratedWithPrefixedIDs(t *testing.T) {
	useCatalog(t, twoCatalogs)
	if err := catalog.SaveLive("gw", "http://x", []catalog.Model{
		{ID: "deepseek/deepseek-chat", Name: "deepseek/deepseek-chat"},
		{ID: "gpt-5.5", Name: "gpt-5.5"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "gw", Catalog: "openai,deepseek"}
	ms := p.Available()
	if len(ms) != 2 || ms[0].Name != "DeepSeek Chat" || ms[1].Name != "GPT-5.5" {
		t.Fatalf("decorated live list: %+v", ms)
	}
	if len(ms[1].Efforts) != 2 {
		t.Errorf("gpt-5.5 efforts: %v", ms[1].Efforts)
	}
}
