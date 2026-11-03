package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/agent"
	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

type genv struct {
	t        *testing.T
	envPath  string
	settings string
}

func gsetup(t *testing.T) *genv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DIAL_ADDR", "127.0.0.1:3425")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1", "m2"}, Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	return &genv{t: t, envPath: filepath.Join(dir, ".env"), settings: filepath.Join(dir, "settings.json")}
}

func (g *genv) field(key string) *agent.Field {
	a, err := agent.Find("gemini")
	if err != nil {
		g.t.Fatal(err)
	}
	f := a.Field(key)
	if f == nil {
		g.t.Fatalf("no field %q", key)
	}
	return f
}

func (g *genv) env(k string) (string, bool) { return edit.GetEnvFile(g.envPath, k) }
func (g *genv) js(k string) (string, bool)  { return edit.GetJSON(g.settings, k) }

func TestAcceptGeminiRouteToGateway(t *testing.T) {
	g := gsetup(t)
	if err := g.field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := g.env("GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url %q", v)
	}
	if v, _ := g.env("GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("api key %q", v)
	}
	if v, _ := g.js("security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("auth %q", v)
	}
	if v, _ := g.js("model.name"); v != "fake/m1" {
		t.Errorf("model %q", v)
	}
	if got := g.field("provider").Get(); got != "dial" {
		t.Errorf("auth shown as %q", got)
	}
	// options for auth include dial
	a, _ := agent.Find("gemini")
	found := false
	for _, o := range a.Field("provider").Options(a.Values()) {
		if o.Value == "dial" {
			found = true
		}
	}
	if !found {
		t.Errorf("dial auth option missing while routed")
	}
}

func TestAcceptGeminiRestoreOriginals(t *testing.T) {
	for _, target := range []string{"gemini-2.5-pro", ""} {
		g := gsetup(t)
		os.WriteFile(g.envPath, []byte("GOOGLE_GEMINI_BASE_URL=https://my.example/proxy\nGEMINI_API_KEY=mykey\nOTHER=1\n"), 0o644)
		os.WriteFile(g.settings, []byte(`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-flash"}}`), 0o644)
		if err := g.field("model").Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		// switching between catalog models must not clobber the saved originals
		if err := g.field("model").Set("fake/m2"); err != nil {
			t.Fatal(err)
		}
		if v, _ := g.js("model.name"); v != "fake/m2" {
			t.Errorf("model %q", v)
		}
		if err := g.field("model").Set(target); err != nil {
			t.Fatal(err)
		}
		if target == "" {
			// default: routing and related settings cleared
			if v, ok := g.env("GOOGLE_GEMINI_BASE_URL"); ok && v == gateway.URL() {
				t.Errorf("still routed")
			}
			if v, _ := g.js("model.name"); v != "" {
				t.Errorf("model not cleared: %q", v)
			}
			if v, _ := g.js("security.auth.selectedType"); v == "gemini-api-key" {
				t.Errorf("auth still gemini-api-key")
			}
			if v, _ := g.env("GEMINI_API_KEY"); v == gateway.Token {
				t.Errorf("gateway token remains")
			}
			continue
		}
		if v, _ := g.env("GOOGLE_GEMINI_BASE_URL"); v != "https://my.example/proxy" {
			t.Errorf("base url %q", v)
		}
		if v, _ := g.env("GEMINI_API_KEY"); v != "mykey" {
			t.Errorf("key %q", v)
		}
		if v, _ := g.env("OTHER"); v != "1" {
			t.Errorf("other env lost: %q", v)
		}
		if v, _ := g.js("security.auth.selectedType"); v != "oauth-personal" {
			t.Errorf("auth %q", v)
		}
		if v, _ := g.js("model.name"); v != target {
			t.Errorf("model %q", v)
		}
	}
}

func TestAcceptGeminiRestoreAbsentRemoved(t *testing.T) {
	g := gsetup(t)
	if err := g.field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := g.field("model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if v, ok := g.env("GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("base url should be removed, got %q", v)
	}
	if v, ok := g.env("GEMINI_API_KEY"); ok {
		t.Errorf("api key should be removed, got %q", v)
	}
	if v, ok := g.js("security.auth.selectedType"); ok && v != "" {
		t.Errorf("auth should be removed, got %q", v)
	}
	if v, _ := g.js("model.name"); v != "gemini-2.5-pro" {
		t.Errorf("model %q", v)
	}
}

func TestAcceptGeminiManualDialAuthAndLabels(t *testing.T) {
	g := gsetup(t)
	err := g.field("auth").Set("dial")
	if err == nil {
		t.Fatal("expected error choosing dial auth while unrouted")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "model") {
		t.Errorf("error should point to picking a model: %v", err)
	}
	// label and key address the same field
	a, _ := agent.Find("gemini")
	if a.Field("auth") == nil || a.Field("model") == nil || a.Field("provider") == nil {
		t.Fatal("fields not found by label/key")
	}
	if a.Field("auth").Key != a.Field("provider").Key {
		t.Errorf("auth label and key differ in target")
	}
	// choosing another auth while routed revokes routing
	if err := g.field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := g.field("auth").Set("google"); err != nil {
		t.Fatal(err)
	}
	if v, ok := g.env("GOOGLE_GEMINI_BASE_URL"); ok && v == gateway.URL() {
		t.Errorf("still routed after choosing google auth")
	}
	if v, _ := g.js("security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("auth %q", v)
	}
}

func TestAcceptGeminiModelOptionsGrouped(t *testing.T) {
	gsetup(t)
	a, _ := agent.Find("gemini")
	opts := a.Field("model").Options(a.Values())
	groups := map[string]bool{}
	hasCatalog := false
	for _, o := range opts {
		groups[o.Group] = true
		if o.Value == "fake/m1" {
			hasCatalog = true
		}
	}
	if !hasCatalog || len(groups) < 2 {
		t.Errorf("expected catalog models and a separate native group: groups=%v catalog=%v", groups, hasCatalog)
	}
}
