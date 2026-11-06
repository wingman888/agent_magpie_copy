package provider

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// fallbackHome isolates magpie's config and cache, so nothing real is read.
func fallbackHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	return home
}

func TestFallbackListIsCleanedAndKept(t *testing.T) {
	fallbackHome(t)
	if err := Save(Provider{ID: "backup", Name: "Backup", Key: "k2", Chat: "http://x/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "http://x/v1", Models: []string{"m1"},
		Fallback: []string{"backup/m2", " backup/m2 ", "", "  ", "backup/m3"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("relay")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"backup/m2", "backup/m3"}; !reflect.DeepEqual(p.Fallback, want) {
		t.Fatalf("fallback = %q; want %q", p.Fallback, want)
	}

	if err := SetFallback("relay", []string{"", "backup/m3", "backup/m3 ", "x/y"}); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); !reflect.DeepEqual(p.Fallback, []string{"backup/m3", "x/y"}) {
		t.Fatalf("after SetFallback: %q", p.Fallback)
	}
	if err := SetFallback("relay", nil); err != nil {
		t.Fatal(err)
	}
	if p, _ = Find("relay"); len(p.Fallback) != 0 {
		t.Fatalf("clearing left %q", p.Fallback)
	}
	if err := SetFallback("nobody", []string{"backup/m2"}); err == nil {
		t.Fatal("an unknown provider must be refused")
	}
}

func TestFallbackTargetsSkipUnusableEntries(t *testing.T) {
	fallbackHome(t)
	if err := Save(Provider{ID: "backup", Name: "Backup", Key: "k2", Chat: "http://x/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "http://x/v1", Models: []string{"m1"},
		Fallback: []string{"missing/m1", "backup", "backup/", "backup/deepseek/deepseek-chat"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("relay")
	var got []string
	for _, tg := range p.FallbackTargets() {
		got = append(got, tg.Provider.ID+"/"+tg.Model)
	}
	if want := []string{"backup/deepseek/deepseek-chat"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %q; want %q", got, want)
	}
}

func TestMultipleCatalogsAndPrefixedNames(t *testing.T) {
	fallbackHome(t)
	// the models.dev cache lives under XDG_CACHE_HOME, a fresh temp dir here
	cache := filepath.Join(os.Getenv("XDG_CACHE_HOME"), "magpie")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	const md = `{
	  "openai": {"id":"openai","name":"OpenAI","models":{
	    "gpt-5.5":{"id":"gpt-5.5","name":"GPT-5.5","reasoning_options":[{"type":"effort","values":["low","high"]}]}}},
	  "deepseek": {"id":"deepseek","name":"DeepSeek","models":{
	    "deepseek-chat":{"id":"deepseek-chat","name":"DeepSeek Chat"}}}
	}`
	if err := os.WriteFile(filepath.Join(cache, "models.json"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()

	p := Provider{ID: "relay", Catalog: "openai, deepseek", Models: []string{"deepseek/deepseek-chat", "gpt-5.5", "unknown-x"}}
	byID := map[string]catalog.Model{}
	for _, m := range p.Exposed() {
		byID[m.ID] = m
	}
	if m := byID["deepseek/deepseek-chat"]; m.Name != "DeepSeek Chat" {
		t.Errorf("deepseek/deepseek-chat named %q; want DeepSeek Chat", m.Name)
	}
	if m := byID["gpt-5.5"]; m.Name != "GPT-5.5" || !reflect.DeepEqual(m.Efforts, []string{"low", "high"}) {
		t.Errorf("gpt-5.5 = %+v; want name and efforts from the openai catalog", m)
	}
	if m := byID["unknown-x"]; m.Name != "unknown-x" || len(m.Efforts) != 0 {
		t.Errorf("unknown-x = %+v; want it passed through bare", m)
	}
	if got := p.Available(); len(got) != 2 {
		t.Errorf("available = %d models; want the one from each catalog", len(got))
	}
	if got := joinCatalogs(" openai ,, deepseek,openai "); got != "openai,deepseek" {
		t.Errorf("joinCatalogs = %q", got)
	}
}
