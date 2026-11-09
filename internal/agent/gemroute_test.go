package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// gemroute: fixtures for the "via dial" Gemini CLI routing tests.

func gemrouteSetup(t *testing.T, settings, env string) (*Agent, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settingsPath := filepath.Join(dir, "settings.json")
	envPath := filepath.Join(dir, ".env")
	if settings != "" {
		if err := os.WriteFile(settingsPath, []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if env != "" {
		if err := os.WriteFile(envPath, []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return gemini(home), settingsPath, envPath
}

func gemrouteRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gemrouteEnvVal(t *testing.T, envPath, key string) (string, bool) {
	t.Helper()
	for _, l := range strings.Split(gemrouteRead(t, envPath), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

func gemrouteSet(t *testing.T, a *Agent, key, v string) error {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	return f.Set(v)
}

const gemrouteSettings = `{
  "other": 1,
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`

func TestGemrouteCatalogModelRoutesAndKeepsUnrelated(t *testing.T) {
	a, settingsPath, envPath := gemrouteSetup(t, gemrouteSettings, "FOO=bar\nGEMINI_API_KEY=real-key\n")
	if err := gemrouteSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, ok := gemrouteEnvVal(t, envPath, "GOOGLE_GEMINI_BASE_URL"); !ok || v != gateway.URL() {
		t.Fatalf("base url = %q %v, want %q", v, ok, gateway.URL())
	}
	if v, _ := gemrouteEnvVal(t, envPath, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatalf("GEMINI_API_KEY = %q, want %q", v, gateway.Token)
	}
	if v, _ := gemrouteEnvVal(t, envPath, "FOO"); v != "bar" {
		t.Fatalf("unrelated FOO line lost: %q", gemrouteRead(t, envPath))
	}
	s := gemrouteRead(t, settingsPath)
	if !strings.Contains(s, `"other": 1`) {
		t.Fatalf("unrelated settings field lost:\n%s", s)
	}
	if v, _ := gemrouteJSON(settingsPath, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Fatalf("selectedType = %q", v)
	}
	if v, _ := gemrouteJSON(settingsPath, "model.name"); v != "fake/m1" {
		t.Fatalf("model.name = %q", v)
	}
	if got := a.Field("provider").Get(); got != "dial" {
		t.Fatalf("auth = %q, want dial", got)
	}
}

func TestGemrouteNativeModelRestores(t *testing.T) {
	a, settingsPath, envPath := gemrouteSetup(t, gemrouteSettings, "FOO=bar\nGEMINI_API_KEY=real-key\n")
	if err := gemrouteSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := gemrouteSet(t, a, "model", "gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := gemrouteJSON(settingsPath, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatalf("selectedType not restored: %q", v)
	}
	if v, _ := gemrouteJSON(settingsPath, "model.name"); v != "gemini-2.5-flash" {
		t.Fatalf("model.name = %q, want the native model", v)
	}
	if v, _ := gemrouteEnvVal(t, envPath, "GEMINI_API_KEY"); v != "real-key" {
		t.Fatalf("GEMINI_API_KEY not restored: %q", v)
	}
	if _, ok := gemrouteEnvVal(t, envPath, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatalf("base url should be removed, env is %q", gemrouteRead(t, envPath))
	}
	if v, _ := gemrouteEnvVal(t, envPath, "FOO"); v != "bar" {
		t.Fatalf("unrelated FOO line lost")
	}
	if got := a.Field("provider").Get(); got == "dial" {
		t.Fatalf("auth still reads dial after native model")
	}
	for k := range stashLoad() {
		if strings.HasPrefix(k, "gemini.") {
			t.Fatalf("stash key %q not cleared after restore", k)
		}
	}
}

func TestGemrouteDefaultClearsAbsentValues(t *testing.T) {
	a, settingsPath, envPath := gemrouteSetup(t, "{}\n", "FOO=bar\n")
	if err := gemrouteSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := gemrouteSet(t, a, "model", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := gemrouteEnvVal(t, envPath, "GEMINI_API_KEY"); ok {
		t.Fatalf("GEMINI_API_KEY should be gone, env is %q", gemrouteRead(t, envPath))
	}
	if _, ok := gemrouteEnvVal(t, envPath, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatalf("base url should be gone, env is %q", gemrouteRead(t, envPath))
	}
	if v, _ := gemrouteEnvVal(t, envPath, "FOO"); v != "bar" {
		t.Fatalf("unrelated FOO line lost")
	}
	if v, ok := gemrouteJSON(settingsPath, "security.auth.selectedType"); ok {
		t.Fatalf("selectedType should be gone, got %q", v)
	}
	if v, ok := gemrouteJSON(settingsPath, "model.name"); ok {
		t.Fatalf("model.name should be gone, got %q", v)
	}
}

func TestGemrouteAuthDialGetAndSet(t *testing.T) {
	a, _, _ := gemrouteSetup(t, "{}\n", "")
	if f := a.Field("auth"); f == nil || f != a.Field("provider") {
		t.Fatalf("auth label should resolve to the provider field")
	}
	if err := gemrouteSet(t, a, "auth", "dial"); err == nil {
		t.Fatalf("Set(dial) must fail when not routed")
	}
	if err := gemrouteSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Fatalf("auth = %q, want dial", got)
	}
	if err := gemrouteSet(t, a, "auth", "dial"); err != nil {
		t.Fatalf("Set(dial) on a routed CLI should succeed: %v", err)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Fatalf("Set(dial) changed the route: %q", got)
	}
}

// gemrouteJSON reads one key of settings.json.
func gemrouteJSON(path, key string) (string, bool) {
	return edit.GetJSON(path, key)
}
