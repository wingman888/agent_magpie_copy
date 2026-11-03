package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func isolatedConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
}

func TestFallbackCleanedAndKept(t *testing.T) {
	isolatedConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := SetFallback("relay", []string{" a/x ", "a/x", "", "b/y", "  "}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil || !reflect.DeepEqual(p.Fallback, []string{"a/x", "b/y"}) {
		t.Fatalf("fallback = %v, %v; want [a/x b/y]", p.Fallback, err)
	}

	// saving the provider again without a list keeps its fallback
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k2", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); !reflect.DeepEqual(p.Fallback, []string{"a/x", "b/y"}) {
		t.Fatalf("after re-save fallback = %v", p.Fallback)
	}

	// an empty list clears it
	if err := SetFallback("relay", []string{""}); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find("relay"); len(p.Fallback) != 0 {
		t.Fatalf("cleared fallback = %v", p.Fallback)
	}

	if err := SetFallback("nobody", []string{"a/x"}); err == nil {
		t.Fatal("SetFallback on an unknown provider should fail")
	}
}

func TestFallbackKeptForSignedInAccount(t *testing.T) {
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	if err := SetFallback("claude", []string{"relay/m", " relay/m ", " "}); err != nil {
		t.Fatal(err)
	}
	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || !reflect.DeepEqual(p.Fallback, []string{"relay/m"}) {
		t.Fatalf("account fallback: %+v %v", p, ok)
	}
}

func TestResolveFallbackEntries(t *testing.T) {
	isolatedConfig(t)
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example.com/v1"}); err != nil {
		t.Fatal(err)
	}
	p, model, ok := ResolveFallback("relay/deepseek/deepseek-chat")
	if !ok || p.ID != "relay" || model != "deepseek/deepseek-chat" {
		t.Fatalf("ResolveFallback = %q, %q, %v", p.ID, model, ok)
	}
	for _, bad := range []string{"relay", "relay/", "/m", "nobody/m", "", "relay/ "} {
		if _, _, ok := ResolveFallback(bad); ok {
			t.Errorf("ResolveFallback(%q) should not resolve", bad)
		}
	}
}

// writeCatalogs puts a models.dev-shaped file where the catalog will find it.
func writeCatalogs(t *testing.T, body string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
}

// Several catalogs are read in the order given; a "provider/model" pick finds
// its name and reasoning levels from the catalog's own id.
func TestMultipleCatalogsAndPrefixedPicks(t *testing.T) {
	writeCatalogs(t, `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5": {"id":"gpt-5.5","name":"GPT-5.5","release_date":"2026-01-01",
	      "reasoning_options":[{"type":"effort","values":["low","high"]}],"modalities":{"output":["text"]}}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat": {"id":"deepseek-chat","name":"DeepSeek Chat","release_date":"2025-01-01",
	      "temperature":false,"modalities":{"output":["text"]}}}}
	}`)
	p := normalize(Provider{ID: "gw", Name: "GW", Key: "k", Chat: "https://gw.example.com/v1",
		Catalog: " OpenAI, deepseek ,openai ", Models: []string{"deepseek/deepseek-chat", "gpt-5.5", "unknown/zzz"}})
	if p.Catalog != "openai,deepseek" || !reflect.DeepEqual(p.Catalogs(), []string{"openai", "deepseek"}) {
		t.Fatalf("catalog = %q, %v", p.Catalog, p.Catalogs())
	}
	ex := p.Exposed()
	if len(ex) != 3 {
		t.Fatalf("exposed %d models, want 3", len(ex))
	}
	if ex[0].ID != "deepseek/deepseek-chat" || ex[0].Name != "DeepSeek Chat" {
		t.Fatalf("prefixed pick = %+v", ex[0])
	}
	if ex[1].ID != "gpt-5.5" || ex[1].Name != "GPT-5.5" || !reflect.DeepEqual(ex[1].Efforts, []string{"low", "high"}) {
		t.Fatalf("gpt-5.5 = %+v", ex[1])
	}
	if ex[2].ID != "unknown/zzz" || ex[2].Name != "unknown/zzz" || len(ex[2].Efforts) != 0 {
		t.Fatalf("unlisted pick = %+v", ex[2])
	}
	if !p.RejectsTemperature("deepseek/deepseek-chat") {
		t.Fatal("prefixed model should inherit the catalog's temperature limit")
	}

	// a single catalog name keeps working as before
	single := normalize(Provider{ID: "one", Catalog: "openai"})
	if single.Catalog != "openai" || len(single.Available()) != 1 {
		t.Fatalf("single catalog: %q with %d models", single.Catalog, len(single.Available()))
	}
}
