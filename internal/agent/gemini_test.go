package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// geminiSandbox points HOME, XDG and the gateway address at test values and
// writes one custom provider ("fake", model m1) so catalog refs resolve.
func geminiSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	t.Setenv("DIAL_ADDR", "127.0.0.1:3999")
	writeTestFile(t, filepath.Join(home, "config", "dial", "providers.json"),
		`{"providers":[{"id":"fake","name":"Fake","chat":"http://127.0.0.1:1/v1","key":"k","models":["m1"]}]}`)
	return home
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const geminiSettings = `{"other":"x","security":{"auth":{"selectedType":"oauth-personal"}}}`
const geminiEnv = "FOO=bar\nGOOGLE_GEMINI_BASE_URL=https://old.example\nGEMINI_API_KEY=oldkey\n"

func geminiFiles(t *testing.T, home, settings, env string) (string, string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	sp, ep := filepath.Join(dir, "settings.json"), filepath.Join(dir, ".env")
	writeTestFile(t, sp, settings)
	writeTestFile(t, ep, env)
	return sp, ep
}

func jsonVal(t *testing.T, path, key string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, key)
	return v
}

func envVal(t *testing.T, path, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(path, key)
}

func setField(t *testing.T, a *Agent, key, v string) {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	if err := f.Set(v); err != nil {
		t.Fatalf("set %s=%q: %v", key, v, err)
	}
}

func TestGeminiRouteToCatalogModel(t *testing.T) {
	home := geminiSandbox(t)
	sp, ep := geminiFiles(t, home, geminiSettings, geminiEnv)
	a := gemini(home)

	setField(t, a, "model", "fake/m1")

	if v, _ := envVal(t, ep, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url = %q, want %q", v, gateway.URL())
	}
	if v, _ := envVal(t, ep, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want %q", v, gateway.Token)
	}
	if v, _ := envVal(t, ep, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if v := jsonVal(t, sp, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q", v)
	}
	if v := jsonVal(t, sp, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q", v)
	}
	if v := jsonVal(t, sp, "other"); v != "x" {
		t.Errorf("unrelated settings field lost: other=%q", v)
	}
	if got := a.Field("provider").Get(); got != "dial" {
		t.Errorf("auth shows %q, want dial", got)
	}
}

func TestGeminiNativeModelRestoresAuth(t *testing.T) {
	home := geminiSandbox(t)
	sp, ep := geminiFiles(t, home, geminiSettings, geminiEnv)
	a := gemini(home)
	setField(t, a, "model", "fake/m1")

	setField(t, a, "model", "gemini-2.5-pro")

	if v := jsonVal(t, sp, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q, want oauth-personal", v)
	}
	if v, _ := envVal(t, ep, "GEMINI_API_KEY"); v != "oldkey" {
		t.Errorf("GEMINI_API_KEY = %q, want oldkey", v)
	}
	if v, _ := envVal(t, ep, "GOOGLE_GEMINI_BASE_URL"); v != "https://old.example" {
		t.Errorf("base url = %q, want the old one", v)
	}
	if v := jsonVal(t, sp, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("model.name = %q, want gemini-2.5-pro", v)
	}
	if v, _ := envVal(t, ep, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if got := a.Field("provider").Get(); got != "custom" {
		t.Errorf("auth shows %q, want custom (old base url)", got)
	}
}

func TestGeminiDefaultRemovesWhatWasAbsent(t *testing.T) {
	home := geminiSandbox(t)
	sp, ep := geminiFiles(t, home, `{"other":"x"}`, "FOO=bar\n")
	a := gemini(home)
	setField(t, a, "model", "fake/m1")

	setField(t, a, "model", "")

	if v := jsonVal(t, sp, "model.name"); v != "" {
		t.Errorf("model.name = %q, want removed", v)
	}
	if v := jsonVal(t, sp, "security.auth.selectedType"); v != "" {
		t.Errorf("selectedType = %q, want removed", v)
	}
	if _, ok := envVal(t, ep, "GEMINI_API_KEY"); ok {
		t.Errorf("GEMINI_API_KEY still set after Default")
	}
	if _, ok := envVal(t, ep, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL still set after Default")
	}
	if v, _ := envVal(t, ep, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if v := jsonVal(t, sp, "other"); v != "x" {
		t.Errorf("unrelated settings field lost: other=%q", v)
	}
}

func TestGeminiAuthDialOnlyWhenRouted(t *testing.T) {
	home := geminiSandbox(t)
	sp, _ := geminiFiles(t, home, geminiSettings, geminiEnv)
	a := gemini(home)
	auth := a.Field("auth") // found by its label
	if auth == nil || auth != &a.Fields[0] {
		t.Fatalf(`field "auth" not found by label`)
	}

	if err := auth.Set("dial"); err == nil {
		t.Errorf(`Set("dial") succeeded while not routed`)
	}
	if got := auth.Get(); got == "dial" {
		t.Errorf("Get() = dial while not routed")
	}

	setField(t, a, "model", "fake/m1")
	if got := auth.Get(); got != "dial" {
		t.Fatalf("routed Get() = %q, want dial", got)
	}
	if err := auth.Set("dial"); err != nil {
		t.Fatalf(`Set("dial") while routed: %v`, err)
	}
	if v := jsonVal(t, sp, "model.name"); v != "fake/m1" {
		t.Errorf("Set(dial) changed model.name to %q", v)
	}
	if v := jsonVal(t, sp, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("Set(dial) changed selectedType to %q", v)
	}
}

func TestGeminiNativeModelLeavesAuthAlone(t *testing.T) {
	home := geminiSandbox(t)
	sp, ep := geminiFiles(t, home, geminiSettings, geminiEnv)
	a := gemini(home)

	setField(t, a, "model", "gemini-2.5-pro")
	setField(t, a, "model", "")

	if v := jsonVal(t, sp, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q, want untouched oauth-personal", v)
	}
	if v, _ := envVal(t, ep, "GOOGLE_GEMINI_BASE_URL"); v != "https://old.example" {
		t.Errorf("base url = %q, want untouched", v)
	}
	if v := jsonVal(t, sp, "model.name"); v != "" {
		t.Errorf("model.name = %q after Default, want removed", v)
	}
}

func TestGeminiModelOptionsIncludeCatalog(t *testing.T) {
	home := geminiSandbox(t)
	geminiFiles(t, home, "{}", "")
	a := gemini(home)
	found := false
	for _, o := range a.Field("model").Options(nil) {
		if o.Value == "fake/m1" {
			found = true
		}
	}
	if !found {
		t.Errorf("model options lack fake/m1")
	}
}
