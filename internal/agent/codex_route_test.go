package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func codexHome(t *testing.T, auth, config string) (home string, read func() string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", "")
	dir := filepath.Join(home, ".codex")
	os.MkdirAll(dir, 0o755)
	if auth != "" {
		os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600)
	}
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o644)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return home, func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "config.toml"))
		return string(b)
	}
}

// Signed in, a magpie model points Codex's built-in OpenAI provider at
// magpie and leaves the provider as it is; one of Codex's own models takes
// the base URL away again and brings back what was there.
func TestCodexSignedInRoutesByBaseURL(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`,
		"model = \"gpt-5.5\"\nmodel_reasoning_effort = \"xhigh\"\n\n[projects.\"/x\"]\ntrust_level = \"trusted\"\n")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, `openai_base_url = "http://127.0.0.1:`) || !strings.Contains(cfg, `/backend-api/codex"`) ||
		!strings.Contains(cfg, `model = "fake/m1"`) || strings.Contains(cfg, "model_provider") ||
		strings.Contains(cfg, "model_catalog_json") || strings.Contains(cfg, "[model_providers") {
		t.Fatalf("magpie model:\n%s", cfg)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "magpie-models.json")); err == nil {
		t.Error("catalog file written")
	}
	if err := cx.Fields[0].Set("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	cfg = read()
	if strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-5.4"`) ||
		!strings.Contains(cfg, `model_reasoning_effort = "xhigh"`) || !strings.Contains(cfg, `trust_level = "trusted"`) {
		t.Fatalf("own model:\n%s", cfg)
	}
}

// A magpie set up as a provider of Codex's, from before, moves to the base
// URL the next time a magpie model is picked.
func TestCodexSignedInLeavesProviderTable(t *testing.T) {
	home, read := codexHome(t, `{"OPENAI_API_KEY":"sk-x"}`,
		"model = \"fake/m1\"\nmodel_provider = \"magpie\"\nmodel_catalog_json = \"/x/magpie-models.json\"\n\n[model_providers.magpie]\nname = \"magpie\"\nbase_url = \"http://127.0.0.1:3425/v1\"\nwire_api = \"responses\"\n")
	if err := codex(home).Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, "openai_base_url") || strings.Contains(cfg, "model_provider") || strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("\n%s", cfg)
	}
}

// Not signed in, Codex's OpenAI provider can't run, so magpie is a provider
// of its own.
func TestCodexSignedOutUsesProvider(t *testing.T) {
	home, read := codexHome(t, "", "")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model_provider = "magpie"`) ||
		!strings.Contains(cfg, "[model_providers.magpie]") || !strings.Contains(cfg, "model_catalog_json") {
		t.Fatalf("\n%s", cfg)
	}
	if err := cx.Fields[0].Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg = read(); strings.Contains(cfg, "magpie") || strings.Contains(cfg, "model") {
		t.Fatalf("reset:\n%s", cfg)
	}
}
