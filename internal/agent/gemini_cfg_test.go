package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// gemCfgHome builds a temp HOME with a Gemini CLI config, a dial providers
// file that has one catalog provider ("fake", model "m1"), and returns the
// Gemini agent bound to that HOME.
func gemCfgHome(t *testing.T, settings, env string) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dialDir := filepath.Join(home, ".config", "dial")
	if err := os.MkdirAll(dialDir, 0o755); err != nil {
		t.Fatal(err)
	}
	providers := `{"providers":[{"id":"fake","name":"Fake","key":"k","chat":"http://127.0.0.1:9/v1","models":["m1"]}]}`
	if err := os.WriteFile(filepath.Join(dialDir, "providers.json"), []byte(providers), 0o600); err != nil {
		t.Fatal(err)
	}
	return gemini(home), dir
}

func gemCfgEnv(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	v, ok := edit.GetEnvFile(filepath.Join(dir, ".env"), key)
	return v, ok
}

func gemCfgJSON(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(filepath.Join(dir, "settings.json"), key)
}

func gemCfgSet(t *testing.T, a *Agent, field, value string) {
	t.Helper()
	f := a.Field(field)
	if f == nil {
		t.Fatalf("no field %q", field)
	}
	if err := f.Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", field, value, err)
	}
}

func TestGemCfgRouteCatalogModel(t *testing.T) {
	a, dir := gemCfgHome(t,
		`{"other":"keep","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`,
		"FOO=bar\n")

	gemCfgSet(t, a, "model", "fake/m1")

	if v, _ := gemCfgEnv(t, dir, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q, want %q", v, gateway.URL())
	}
	if v, _ := gemCfgEnv(t, dir, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want %q", v, gateway.Token)
	}
	if v, _ := gemCfgEnv(t, dir, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO = %q", v)
	}
	if v, _ := gemCfgJSON(t, dir, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", v)
	}
	if v, _ := gemCfgJSON(t, dir, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", v)
	}
	if v, _ := gemCfgJSON(t, dir, "other"); v != "keep" {
		t.Errorf("unrelated settings field lost: other = %q", v)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Errorf("auth = %q, want dial", got)
	}
	if got := a.Field("provider").Get(); got != "fake" {
		t.Errorf("provider = %q, want fake", got)
	}

	// dial cannot be set by hand, but setting it while routed changes nothing
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Errorf("Set(dial) while routed: %v", err)
	}
	if v, _ := gemCfgJSON(t, dir, "model.name"); v != "fake/m1" {
		t.Errorf("Set(dial) changed model.name to %q", v)
	}
	if v, _ := gemCfgEnv(t, dir, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("Set(dial) changed base URL to %q", v)
	}
}

func TestGemCfgAuthDialRequiresRouting(t *testing.T) {
	a, _ := gemCfgHome(t, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "")
	if err := a.Field("auth").Set("dial"); err == nil {
		t.Error("Set(dial) on an unrouted CLI should fail")
	}
	if got := a.Field("auth").Get(); got == "dial" {
		t.Error("auth reads dial without routing")
	}
}

func TestGemCfgNativeModelRestores(t *testing.T) {
	a, dir := gemCfgHome(t,
		`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`,
		"FOO=bar\n")
	gemCfgSet(t, a, "model", "fake/m1")
	gemCfgSet(t, a, "model", "gemini-2.5-flash")

	if v, _ := gemCfgJSON(t, dir, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q, want the pre-route oauth-personal", v)
	}
	if v, ok := gemCfgEnv(t, dir, "GEMINI_API_KEY"); ok {
		t.Errorf("GEMINI_API_KEY should be removed, got %q", v)
	}
	if v, ok := gemCfgEnv(t, dir, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL should be removed, got %q", v)
	}
	if v, _ := gemCfgJSON(t, dir, "model.name"); v != "gemini-2.5-flash" {
		t.Errorf("model.name = %q, want gemini-2.5-flash", v)
	}
	if v, _ := gemCfgEnv(t, dir, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO = %q", v)
	}
	if got := a.Field("auth").Get(); got != "google" {
		t.Errorf("auth = %q, want google", got)
	}
}

func TestGemCfgDefaultRestoresPriorKeyAndBase(t *testing.T) {
	a, dir := gemCfgHome(t,
		`{"security":{"auth":{"selectedType":"gemini-api-key"}}}`,
		"GEMINI_API_KEY=real-key\nGOOGLE_GEMINI_BASE_URL=https://old.example\n")
	gemCfgSet(t, a, "model", "fake/m1")
	gemCfgSet(t, a, "model", "")

	if v, _ := gemCfgEnv(t, dir, "GEMINI_API_KEY"); v != "real-key" {
		t.Errorf("GEMINI_API_KEY = %q, want real-key", v)
	}
	if v, _ := gemCfgEnv(t, dir, "GOOGLE_GEMINI_BASE_URL"); v != "https://old.example" {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q, want https://old.example", v)
	}
	if v, _ := gemCfgJSON(t, dir, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", v)
	}
	if _, ok := gemCfgJSON(t, dir, "model.name"); ok {
		t.Error("model.name should be removed by Default")
	}
}

func TestGemCfgDefaultWithoutPriorKey(t *testing.T) {
	a, dir := gemCfgHome(t, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "")
	gemCfgSet(t, a, "model", "fake/m1")
	gemCfgSet(t, a, "model", "")

	if v, ok := gemCfgEnv(t, dir, "GEMINI_API_KEY"); ok {
		t.Errorf("GEMINI_API_KEY should be absent after Default, got %q", v)
	}
	if v, _ := gemCfgJSON(t, dir, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q, want oauth-personal", v)
	}
	if _, ok := gemCfgJSON(t, dir, "model.name"); ok {
		t.Error("model.name should be removed by Default")
	}
}

func TestGemCfgProviderFieldKeepsModel(t *testing.T) {
	a, dir := gemCfgHome(t, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "")
	gemCfgSet(t, a, "model", "fake/m1")
	gemCfgSet(t, a, "provider", "")
	if _, ok := gemCfgJSON(t, dir, "model.name"); ok {
		t.Error("provider Default should clear the model")
	}
	gemCfgSet(t, a, "model", "fake/m1")
	if err := a.Field("provider").Set("nosuch"); err == nil {
		t.Error("provider nosuch should fail: dial has no such model")
	}
}
