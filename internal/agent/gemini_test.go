package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// geminiHome points HOME and the dial config at a fresh directory, registers
// one catalog provider ("fake" with model m1) and returns the Gemini agent.
func agm_geminiHome(t *testing.T) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:9/v1",
		Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return gemini(home), home
}

func agm_writeGeminiFiles(t *testing.T, home, settings, env string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
}

const agm_geminiSettings = `{
  "other": "keep",
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`

const agm_geminiEnv = "FOO=bar\nGOOGLE_GEMINI_BASE_URL=https://generativelanguage.googleapis.com\nGEMINI_API_KEY=AIza-real\n"

func agm_geminiFileValues(t *testing.T, home string) (sel, model, other string, env map[string]string) {
	t.Helper()
	s := filepath.Join(home, ".gemini", "settings.json")
	sel, _ = edit.GetJSON(s, "security.auth.selectedType")
	model, _ = edit.GetJSON(s, "model.name")
	other, _ = edit.GetJSON(s, "other")
	env = map[string]string{}
	e := filepath.Join(home, ".gemini", ".env")
	for _, k := range []string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY", "FOO"} {
		if v, ok := edit.GetEnvFile(e, k); ok {
			env[k] = v
		}
	}
	return
}

func TestAgm_GeminiRoutesCatalogModel(t *testing.T) {
	a, home := agm_geminiHome(t)
	agm_writeGeminiFiles(t, home, agm_geminiSettings, agm_geminiEnv)

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	sel, model, other, env := agm_geminiFileValues(t, home)
	if sel != "gemini-api-key" || model != "fake/m1" || other != "keep" {
		t.Fatalf("settings: selectedType=%q model=%q other=%q", sel, model, other)
	}
	if env["GOOGLE_GEMINI_BASE_URL"] != gateway.URL() {
		t.Fatalf("base url = %q, want %q", env["GOOGLE_GEMINI_BASE_URL"], gateway.URL())
	}
	if env["GEMINI_API_KEY"] != gateway.Token {
		t.Fatalf("key = %q, want gateway token", env["GEMINI_API_KEY"])
	}
	if env["FOO"] != "bar" {
		t.Fatalf("unrelated .env line lost: %v", env)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Fatalf("auth = %q, want dial", got)
	}
	if got := a.Field("provider").Get(); got != "fake" {
		t.Fatalf("provider = %q, want fake", got)
	}
}

func TestAgm_GeminiNativeModelRestores(t *testing.T) {
	a, home := agm_geminiHome(t)
	agm_writeGeminiFiles(t, home, agm_geminiSettings, agm_geminiEnv)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	sel, model, _, env := agm_geminiFileValues(t, home)
	if sel != "oauth-personal" {
		t.Errorf("selectedType = %q, want oauth-personal", sel)
	}
	if model != "gemini-2.5-flash" {
		t.Errorf("model = %q, want the native model just picked", model)
	}
	if env["GEMINI_API_KEY"] != "AIza-real" {
		t.Errorf("key = %q, want AIza-real", env["GEMINI_API_KEY"])
	}
	if env["GOOGLE_GEMINI_BASE_URL"] != "https://generativelanguage.googleapis.com" {
		t.Errorf("base = %q, want the original", env["GOOGLE_GEMINI_BASE_URL"])
	}
	if env["FOO"] != "bar" {
		t.Errorf("unrelated .env line lost: %v", env)
	}
	if got := a.Field("auth").Get(); got == "dial" {
		t.Errorf("auth still shows dial after a native model")
	}
}

func TestAgm_GeminiDefaultRemovesWhatRoutingAdded(t *testing.T) {
	a, home := agm_geminiHome(t)
	// no key, no base URL, no selectedType before routing
	agm_writeGeminiFiles(t, home, `{"other": "keep", "model": {"name": "gemini-2.5-pro"}}`, "FOO=bar\n")
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	sel, model, other, env := agm_geminiFileValues(t, home)
	if sel != "" {
		t.Errorf("selectedType = %q, want removed", sel)
	}
	if model != "" {
		t.Errorf("model.name = %q, want removed", model)
	}
	if other != "keep" {
		t.Errorf("unrelated settings field lost")
	}
	if _, ok := env["GEMINI_API_KEY"]; ok {
		t.Errorf("GEMINI_API_KEY should be gone, got %q", env["GEMINI_API_KEY"])
	}
	if _, ok := env["GOOGLE_GEMINI_BASE_URL"]; ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL should be gone")
	}
	if env["FOO"] != "bar" {
		t.Errorf("unrelated .env line lost: %v", env)
	}
}

func TestAgm_GeminiDefaultRestoresPriorValues(t *testing.T) {
	a, home := agm_geminiHome(t)
	agm_writeGeminiFiles(t, home, agm_geminiSettings, agm_geminiEnv)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	sel, model, _, env := agm_geminiFileValues(t, home)
	if sel != "oauth-personal" || model != "" {
		t.Errorf("selectedType=%q model=%q, want oauth-personal and no model", sel, model)
	}
	if env["GEMINI_API_KEY"] != "AIza-real" || env["GOOGLE_GEMINI_BASE_URL"] != "https://generativelanguage.googleapis.com" {
		t.Errorf("prior key/base not restored: %v", env)
	}
}

func TestAgm_GeminiAuthGetSet(t *testing.T) {
	a, home := agm_geminiHome(t)
	agm_writeGeminiFiles(t, home, agm_geminiSettings, agm_geminiEnv)
	auth := a.Field("auth")

	if err := auth.Set("dial"); err == nil {
		t.Fatal("Set(dial) without routing should fail")
	}
	if got := auth.Get(); got == "dial" {
		t.Fatalf("auth = dial without routing")
	}

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if got := auth.Get(); got != "dial" {
		t.Fatalf("auth = %q while routed, want dial", got)
	}
	if err := auth.Set("dial"); err != nil {
		t.Fatalf("Set(dial) while routed: %v", err)
	}
	sel, model, _, env := agm_geminiFileValues(t, home)
	if sel != "gemini-api-key" || model != "fake/m1" || env["GEMINI_API_KEY"] != gateway.Token {
		t.Errorf("Set(dial) changed the route: selectedType=%q model=%q key=%q", sel, model, env["GEMINI_API_KEY"])
	}
}

func TestAgm_GeminiLabelLookup(t *testing.T) {
	a, _ := agm_geminiHome(t)
	if f := a.Field("auth"); f == nil || f.Key != "auth" {
		t.Fatalf("Field(auth) = %+v", f)
	}
	if f := a.Field("provider"); f == nil {
		t.Fatal("Field(provider) missing")
	}
	// a label that is not a key: crush shows its model field as "large"
	for _, c := range All() {
		if c.ID != "crush" {
			continue
		}
		if f := c.Field("large"); f == nil || f.Key != "model" {
			t.Fatalf("crush Field(large) = %+v, want the model field", f)
		}
	}
}

func TestAgm_GeminiProviderField(t *testing.T) {
	a, home := agm_geminiHome(t)
	agm_writeGeminiFiles(t, home, agm_geminiSettings, agm_geminiEnv)
	p := a.Field("provider")
	if got := p.Get(); got != "" {
		t.Fatalf("provider = %q before routing, want empty", got)
	}
	if err := p.Set("fake"); err != nil {
		t.Fatal(err)
	}
	if _, model, _, _ := agm_geminiFileValues(t, home); model != "fake/m1" {
		t.Fatalf("model = %q, want fake/m1", model)
	}
	if err := p.Set("nope"); err == nil {
		t.Fatal("unknown provider should fail")
	}
	found := false
	for _, o := range p.Options(nil) {
		if o.Value == "fake" && strings.Contains(o.Note, "via dial") {
			found = true
		}
	}
	if !found {
		t.Fatal("provider options should list fake")
	}
}

func TestAgm_GeminiModelOptionsIncludeDial(t *testing.T) {
	a, home := agm_geminiHome(t)
	agm_writeGeminiFiles(t, home, agm_geminiSettings, agm_geminiEnv)
	found := false
	for _, o := range a.Field("model").Options(nil) {
		if o.Value == "fake/m1" {
			found = true
		}
	}
	if !found {
		t.Fatal("model options should offer fake/m1 through dial")
	}
}
