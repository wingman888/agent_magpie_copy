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

// geminiEnv sets up an isolated home and dial config with one catalog
// provider ("fake", models m1 and m2), and returns the home and the agent.
func geminiEnv(t *testing.T, env, settings string) (string, *Agent) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:9/v1",
		Key: "k", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, gemini(home)
}

func geminiFiles(home string) (base, key, auth, model string) {
	dir := filepath.Join(home, ".gemini")
	envPath := filepath.Join(dir, ".env")
	path := filepath.Join(dir, "settings.json")
	base, _ = edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	key, _ = edit.GetEnvFile(envPath, "GEMINI_API_KEY")
	auth, _ = edit.GetJSON(path, "security.auth.selectedType")
	model, _ = edit.GetJSON(path, "model.name")
	return
}

func setField(t *testing.T, a *Agent, key, value string) {
	t.Helper()
	if err := a.Field(key).Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", key, value, err)
	}
}

// (a) choosing a catalog model writes the gateway routing and shows as dial.
func TestGeminiRouteWritesGatewayConfig(t *testing.T) {
	home, a := geminiEnv(t, "", "")
	setField(t, a, "model", "fake/m1")

	base, key, auth, model := geminiFiles(home)
	if base != gateway.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q, want %q", base, gateway.URL())
	}
	if key != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want %q", key, gateway.Token)
	}
	if auth != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", auth)
	}
	if model != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", model)
	}
	if got := a.Field("provider").Get(); got != "dial" {
		t.Errorf("auth shows %q, want dial", got)
	}
}

// (a) the auth field keeps other keys in both files intact.
func TestGeminiRouteKeepsOtherKeys(t *testing.T) {
	home, a := geminiEnv(t, "FOO=bar\n", `{"theme":"dark"}`)
	setField(t, a, "model", "fake/m1")
	dir := filepath.Join(home, ".gemini")
	if v, _ := edit.GetEnvFile(filepath.Join(dir, ".env"), "FOO"); v != "bar" {
		t.Errorf("FOO = %q, want bar", v)
	}
	if v, _ := edit.GetJSON(filepath.Join(dir, "settings.json"), "theme"); v != "dark" {
		t.Errorf("theme = %q, want dark", v)
	}
}

// (b) a user with their own base URL, key and model gets all of them back
// when leaving the gateway for a native model.
func TestGeminiUnrouteRestoresOriginalsForNativeModel(t *testing.T) {
	env := "GOOGLE_GEMINI_BASE_URL=https://my.proxy\nGEMINI_API_KEY=own-key\n"
	settings := `{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`
	home, a := geminiEnv(t, env, settings)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "gemini-2.5-flash")

	base, key, auth, model := geminiFiles(home)
	if base != "https://my.proxy" || key != "own-key" || auth != "gemini-api-key" {
		t.Errorf("restored env/auth = (%q, %q, %q), want original", base, key, auth)
	}
	if model != "gemini-2.5-flash" {
		t.Errorf("model.name = %q, want the native choice gemini-2.5-flash", model)
	}
}

// (b) choosing default while routed restores the original model.
func TestGeminiDefaultRestoresOriginalModel(t *testing.T) {
	env := "GOOGLE_GEMINI_BASE_URL=https://my.proxy\nGEMINI_API_KEY=own-key\n"
	settings := `{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`
	home, a := geminiEnv(t, env, settings)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "")

	base, key, auth, model := geminiFiles(home)
	if base != "https://my.proxy" || key != "own-key" || auth != "gemini-api-key" || model != "gemini-2.5-pro" {
		t.Errorf("after default = (%q, %q, %q, %q), want originals", base, key, auth, model)
	}
}

// (b) values that were absent before routing are removed again.
func TestGeminiUnrouteRemovesAbsentValues(t *testing.T) {
	home, a := geminiEnv(t, "FOO=bar\n", `{"theme":"dark"}`)
	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "")

	base, key, auth, model := geminiFiles(home)
	if base != "" || key != "" || auth != "" || model != "" {
		t.Errorf("after default = (%q, %q, %q, %q), want all absent", base, key, auth, model)
	}
	dir := filepath.Join(home, ".gemini")
	raw, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if strings.Contains(string(raw), "GOOGLE_GEMINI_BASE_URL") || !strings.Contains(string(raw), "FOO=bar") {
		t.Errorf(".env = %q", raw)
	}
	if v, _ := edit.GetJSON(filepath.Join(dir, "settings.json"), "theme"); v != "dark" {
		t.Errorf("theme = %q, want dark", v)
	}
}

// (b) a Google sign-in survives a round trip through the gateway.
func TestGeminiGoogleSignInSurvivesRoundTrip(t *testing.T) {
	settings := `{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`
	home, a := geminiEnv(t, "", settings)

	setField(t, a, "model", "fake/m2")
	if got := a.Field("provider").Get(); got != "dial" {
		t.Fatalf("while routed auth shows %q, want dial", got)
	}
	setField(t, a, "provider", "google")

	base, key, auth, model := geminiFiles(home)
	if base != "" || key != "" || auth != "oauth-personal" || model != "gemini-2.5-pro" {
		t.Errorf("after google = (%q, %q, %q, %q), want original sign-in", base, key, auth, model)
	}
}

// (c) switching between catalog models keeps the first-saved originals,
// and switching back to a native model restores them.
func TestGeminiRerouteKeepsOriginals(t *testing.T) {
	env := "GOOGLE_GEMINI_BASE_URL=https://my.proxy\nGEMINI_API_KEY=own-key\n"
	settings := `{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`
	home, a := geminiEnv(t, env, settings)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "fake/m2")
	if got := a.Field("model").Get(); got != "fake/m2" {
		t.Fatalf("model = %q, want fake/m2", got)
	}
	setField(t, a, "model", "gemini-2.5-pro")

	base, key, auth, model := geminiFiles(home)
	if base != "https://my.proxy" || key != "own-key" || auth != "oauth-personal" || model != "gemini-2.5-pro" {
		t.Errorf("after native = (%q, %q, %q, %q), want first-saved originals", base, key, auth, model)
	}
}

// (d) "dial" as an auth value is refused while not routed, with guidance.
func TestGeminiManualDialAuthNeedsRouting(t *testing.T) {
	_, a := geminiEnv(t, "", "")
	err := a.Field("auth").Set("dial")
	if err == nil || !strings.Contains(err.Error(), "dial gemini model") {
		t.Fatalf("set dial while not routed: err = %v, want guidance to pick a catalog model", err)
	}
}

// (d) the display name "auth" addresses the same field as "provider".
func TestGeminiFieldByDisplayName(t *testing.T) {
	_, a := geminiEnv(t, "", "")
	if a.Field("auth") == nil || a.Field("auth") != a.Field("provider") {
		t.Fatal(`Field("auth") does not resolve to the provider field`)
	}
	if a.Field("model") == nil || a.Field("nope") != nil {
		t.Fatal("model lookup or unknown-field handling broken")
	}
}

// (d) choosing auth by display name routes the same way as by key.
func TestGeminiAuthByDisplayNameSetsSameValue(t *testing.T) {
	home, a := geminiEnv(t, "", `{"security":{"auth":{"selectedType":"oauth-personal"}}}`)
	setField(t, a, "auth", "vertex")
	_, _, auth, _ := geminiFiles(home)
	if auth != "vertex-ai" {
		t.Errorf("selectedType = %q, want vertex-ai", auth)
	}
}

// Model options list native Google models and catalog models in groups.
func TestGeminiModelOptionsGrouped(t *testing.T) {
	_, a := geminiEnv(t, "", "")
	var catalogOpt *Option
	for _, o := range a.Field("model").Options(nil) {
		if o.Value == "fake/m1" {
			o := o
			catalogOpt = &o
		}
	}
	if catalogOpt == nil {
		t.Fatal("catalog model fake/m1 missing from model options")
	}
	if catalogOpt.Group != "Fake" {
		t.Errorf("catalog group = %q, want Fake", catalogOpt.Group)
	}
}
