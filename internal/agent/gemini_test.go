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

// geminiSandbox points HOME and dial's config at temp dirs and registers a
// fake provider "fake" with models m1 and m2.
func geminiSandbox(t *testing.T) (home string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	if err := provider.Save(provider.Provider{
		ID: "fake", Name: "Fake", Chat: "https://fake.example.com/v1", Key: "k",
		Models: []string{"m1", "m2"},
	}); err != nil {
		t.Fatal(err)
	}
	return home
}

func writeGemini(t *testing.T, home, settings, env string) (settingsPath, envPath string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath = filepath.Join(dir, "settings.json")
	envPath = filepath.Join(dir, ".env")
	if settings != "" {
		if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if env != "" {
		if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return settingsPath, envPath
}

func getJSON(t *testing.T, path, key string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, key)
	return v
}

func getEnv(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(path, key)
}

func geminiField(t *testing.T, home, key string) *Field {
	t.Helper()
	a := gemini(home)
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	return f
}

const geminiSettings = `{
  "other": "keep me",
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`

const geminiEnv = "FOO=bar\nGEMINI_API_KEY=orig-key\nGOOGLE_GEMINI_BASE_URL=https://old.example.com\n"

func TestGeminiRouteThenNativeRestores(t *testing.T) {
	home := geminiSandbox(t)
	settings, env := writeGemini(t, home, geminiSettings, geminiEnv)

	model := geminiField(t, home, "model")
	auth := geminiField(t, home, "provider")

	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := getEnv(t, env, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url = %q, want %q", v, gateway.URL())
	}
	if v, _ := getEnv(t, env, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("key = %q, want %q", v, gateway.Token)
	}
	if v, _ := getEnv(t, env, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if v := getJSON(t, settings, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q", v)
	}
	if v := getJSON(t, settings, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", v)
	}
	if v := getJSON(t, settings, "other"); v != "keep me" {
		t.Errorf("unrelated settings field lost: other=%q", v)
	}
	if v := auth.Get(); v != "dial" {
		t.Errorf("auth Get = %q, want dial", v)
	}

	// picking a native model restores the CLI's own endpoint and key
	if err := model.Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v := getJSON(t, settings, "model.name"); v != "gemini-2.5-flash" {
		t.Errorf("model.name = %q, want the native pick", v)
	}
	if v := getJSON(t, settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q, want oauth-personal", v)
	}
	if v, _ := getEnv(t, env, "GEMINI_API_KEY"); v != "orig-key" {
		t.Errorf("key = %q, want orig-key", v)
	}
	if v, _ := getEnv(t, env, "GOOGLE_GEMINI_BASE_URL"); v != "https://old.example.com" {
		t.Errorf("base = %q, want the old one", v)
	}
	if auth.Get() == "dial" {
		t.Error("still routed after native pick")
	}
}

func TestGeminiRouteAgainKeepsOriginalStash(t *testing.T) {
	home := geminiSandbox(t)
	settings, env := writeGemini(t, home, geminiSettings, geminiEnv)
	model := geminiField(t, home, "model")

	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if v := getJSON(t, settings, "model.name"); v != "fake/m2" {
		t.Errorf("model.name = %q, want fake/m2", v)
	}
	// Default must bring back the pre-dial values, not the gateway's own.
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	if v, _ := getEnv(t, env, "GEMINI_API_KEY"); v != "orig-key" {
		t.Errorf("key = %q, want orig-key", v)
	}
	if v, _ := getEnv(t, env, "GOOGLE_GEMINI_BASE_URL"); v != "https://old.example.com" {
		t.Errorf("base = %q, want the old one", v)
	}
	if v := getJSON(t, settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q, want oauth-personal", v)
	}
	if v := getJSON(t, settings, "model.name"); v != "" {
		t.Errorf("model.name = %q, want it removed", v)
	}
	if v := getJSON(t, settings, "other"); v != "keep me" {
		t.Errorf("unrelated settings field lost: other=%q", v)
	}
}

func TestGeminiDefaultRemovesThingsThatWereAbsent(t *testing.T) {
	home := geminiSandbox(t)
	settings, env := writeGemini(t, home, `{"other": 1}`, "FOO=bar\n")
	model := geminiField(t, home, "model")

	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := getEnv(t, env, "GEMINI_API_KEY"); ok {
		t.Error("GEMINI_API_KEY should be gone: there was none before routing")
	}
	if _, ok := getEnv(t, env, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Error("GOOGLE_GEMINI_BASE_URL should be gone")
	}
	if v := getJSON(t, settings, "security.auth.selectedType"); v != "" {
		t.Errorf("selectedType = %q, want it removed", v)
	}
	if v := getJSON(t, settings, "model.name"); v != "" {
		t.Errorf("model.name = %q, want it removed", v)
	}
	if v, _ := getEnv(t, env, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if v := getJSON(t, settings, "other"); v != "1" {
		t.Errorf("unrelated settings field lost: other=%q", v)
	}
}

func TestGeminiNativeOnFreshConfigClearsRouting(t *testing.T) {
	home := geminiSandbox(t)
	settings, env := writeGemini(t, home, "", "")
	model := geminiField(t, home, "model")

	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if _, ok := getEnv(t, env, "GEMINI_API_KEY"); ok {
		t.Error("GEMINI_API_KEY should be gone")
	}
	if _, ok := getEnv(t, env, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Error("GOOGLE_GEMINI_BASE_URL should be gone")
	}
	if v := getJSON(t, settings, "security.auth.selectedType"); v != "" {
		t.Errorf("selectedType = %q, want it removed", v)
	}
	if v := getJSON(t, settings, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("model.name = %q", v)
	}
}

func TestGeminiAuthDialGetAndSet(t *testing.T) {
	home := geminiSandbox(t)
	writeGemini(t, home, geminiSettings, geminiEnv)
	auth := geminiField(t, home, "provider")
	model := geminiField(t, home, "model")

	// not routed yet: Set("dial") is an error and changes nothing
	if err := auth.Set("dial"); err == nil {
		t.Error("Set(dial) while not routed should fail")
	}
	if v := auth.Get(); v == "dial" {
		t.Errorf("Get = dial while not routed")
	}

	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	// routed: Set("dial") succeeds and leaves the state alone
	if err := auth.Set("dial"); err != nil {
		t.Fatalf("Set(dial) while routed: %v", err)
	}
	if v := auth.Get(); v != "dial" {
		t.Errorf("Get = %q, want dial", v)
	}
	if v := getJSON(t, filepath.Join(home, ".gemini", "settings.json"), "model.name"); v != "fake/m1" {
		t.Errorf("Set(dial) changed the model to %q", v)
	}
}

func TestGeminiFieldLookup(t *testing.T) {
	home := geminiSandbox(t)
	a := gemini(home)
	for _, k := range []string{"provider", "auth", "Auth", "model"} {
		if a.Field(k) == nil {
			t.Errorf("Field(%q) not found", k)
		}
	}
	if f := a.Field("auth"); f == nil || f.Key != "provider" {
		t.Errorf("auth should resolve to the provider field, got %+v", f)
	}
}

func TestGeminiOptionsIncludeDialModels(t *testing.T) {
	home := geminiSandbox(t)
	opts := geminiField(t, home, "model").Options(nil)
	var found bool
	for _, o := range opts {
		if o.Value == "fake/m1" {
			found = true
			if !strings.Contains(o.Note, "via dial") {
				t.Errorf("note = %q", o.Note)
			}
		}
	}
	if !found {
		t.Error("fake/m1 missing from model options")
	}
}
