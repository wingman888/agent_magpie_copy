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

// geminiSetup gives the test a clean HOME and dial config dir, one ready
// catalog provider ("fake" with models m1 and m2), and the Gemini agent.
func geminiSetup(t *testing.T) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o755); err != nil {
		t.Fatal(err)
	}
	return a, home
}

func geminiWrite(t *testing.T, home, settings, env string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
}

// geminiState reads the four routed values; "" means absent.
func geminiState(t *testing.T, home string) (base, key, auth, model string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	base, _ = edit.GetEnvFile(filepath.Join(dir, ".env"), "GOOGLE_GEMINI_BASE_URL")
	key, _ = edit.GetEnvFile(filepath.Join(dir, ".env"), "GEMINI_API_KEY")
	auth, _ = edit.GetJSON(filepath.Join(dir, "settings.json"), "security.auth.selectedType")
	model, _ = edit.GetJSON(filepath.Join(dir, "settings.json"), "model.name")
	return
}

func mustSet(t *testing.T, a *Agent, key, value string) {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	if err := f.Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", key, value, err)
	}
}

const geminiOrigSettings = `{
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`

func TestGeminiFieldDisplayNameMatchesKey(t *testing.T) {
	a, home := geminiSetup(t)
	if a.Field("auth") == nil || a.Field("auth") != a.Field("provider") {
		t.Fatalf("auth should resolve to the provider field")
	}
	if a.Field("model") == nil || a.Field("model") != a.Field("model") {
		t.Fatalf("model should resolve")
	}
	if a.Field("model") == nil || a.Field("model").Key != "model" {
		t.Fatalf("model should resolve by key")
	}
	if a.Field("nope") != nil {
		t.Fatalf("unknown name should not resolve")
	}
	// the display name and the key write the same thing
	geminiWrite(t, home, "{}\n", "")
	mustSet(t, a, "auth", "google")
	_, _, byName, _ := geminiState(t, home)

	geminiWrite(t, home, "{}\n", "")
	mustSet(t, a, "provider", "google")
	_, _, byKey, _ := geminiState(t, home)
	if byName != "oauth-personal" || byName != byKey {
		t.Fatalf("name gave %q, key gave %q", byName, byKey)
	}
}

func TestGeminiRouteWritesGatewayValues(t *testing.T) {
	a, home := geminiSetup(t)
	geminiWrite(t, home, "{}\n", "")
	mustSet(t, a, "model", "fake/m1")

	base, key, auth, model := geminiState(t, home)
	if base != gateway.URL() || key != gateway.Token || auth != "gemini-api-key" || model != "fake/m1" {
		t.Fatalf("routed values: base=%q key=%q auth=%q model=%q", base, key, auth, model)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Fatalf("auth shows %q, want dial", got)
	}
	if got := a.Field("model").Get(); got != "fake/m1" {
		t.Fatalf("model shows %q", got)
	}
}

func TestGeminiRestoreOriginalValues(t *testing.T) {
	a, home := geminiSetup(t)
	geminiWrite(t, home, geminiOrigSettings, "GOOGLE_GEMINI_BASE_URL=https://old.example\nGEMINI_API_KEY=orig-key\n")
	mustSet(t, a, "model", "fake/m1")

	// switching to another catalog model keeps the first originals
	mustSet(t, a, "model", "fake/m2")
	mustSet(t, a, "model", "gemini-2.5-flash")

	base, key, auth, model := geminiState(t, home)
	if base != "https://old.example" || key != "orig-key" || auth != "oauth-personal" || model != "gemini-2.5-flash" {
		t.Fatalf("restored: base=%q key=%q auth=%q model=%q", base, key, auth, model)
	}
}

func TestGeminiRestoreAbsentValuesAreRemoved(t *testing.T) {
	a, home := geminiSetup(t)
	geminiWrite(t, home, "{}\n", "")
	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "")

	env, _ := os.ReadFile(filepath.Join(home, ".gemini", ".env"))
	settings, _ := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	for _, k := range []string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"} {
		if strings.Contains(string(env), k) {
			t.Fatalf("%s should be removed, .env is %q", k, env)
		}
	}
	for _, k := range []string{"selectedType", "model"} {
		if strings.Contains(string(settings), k) {
			t.Fatalf("%s should be removed, settings is %s", k, settings)
		}
	}
}

func TestGeminiSecondRouteDoesNotOverwriteOriginals(t *testing.T) {
	a, home := geminiSetup(t)
	geminiWrite(t, home, geminiOrigSettings, "GEMINI_API_KEY=mine\n")
	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "fake/m2")
	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "auth", "dial") // already routed: no-op, not an error
	mustSet(t, a, "model", "")    // default restores the originals

	base, key, auth, model := geminiState(t, home)
	if base != "" || key != "mine" || auth != "oauth-personal" || model != "gemini-2.5-pro" {
		t.Fatalf("after default: base=%q key=%q auth=%q model=%q", base, key, auth, model)
	}
}

func TestGeminiManualDialWhenNotRoutedFails(t *testing.T) {
	a, home := geminiSetup(t)
	geminiWrite(t, home, geminiOrigSettings, "")
	err := a.Field("auth").Set("dial")
	if err == nil || !strings.Contains(err.Error(), "catalog model") {
		t.Fatalf("want guidance error, got %v", err)
	}
	if _, _, auth, _ := geminiState(t, home); auth != "oauth-personal" {
		t.Fatalf("failed dial must not change auth, got %q", auth)
	}
}

func TestGeminiAuthChangeUnroutes(t *testing.T) {
	a, home := geminiSetup(t)
	geminiWrite(t, home, geminiOrigSettings, "GEMINI_API_KEY=mine\n")
	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "auth", "google")

	base, key, auth, model := geminiState(t, home)
	if base != "" || key != "mine" || auth != "oauth-personal" || model != "gemini-2.5-pro" {
		t.Fatalf("after google: base=%q key=%q auth=%q model=%q", base, key, auth, model)
	}
}

func TestGeminiDefaultAuthUnroutesAndKeepsOriginals(t *testing.T) {
	a, home := geminiSetup(t)
	geminiWrite(t, home, geminiOrigSettings, "")
	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "auth", "")

	_, _, auth, model := geminiState(t, home)
	if auth != "oauth-personal" || model != "gemini-2.5-pro" {
		t.Fatalf("after default auth: auth=%q model=%q", auth, model)
	}
}
