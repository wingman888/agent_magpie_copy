package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// gemRouteEnv gives the test its own HOME and dial config dir, saves one
// catalog provider ("fake") exposing the given models, and returns the
// Gemini agent plus its settings.json and .env paths.
func gemRouteEnv(t *testing.T, models ...string) (*Agent, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:9/v1", Models: models}); err != nil {
		t.Fatal(err)
	}
	return gemini(home), filepath.Join(home, ".gemini", "settings.json"), filepath.Join(home, ".gemini", ".env")
}

// gemRouteWrite seeds a config file, creating its directory.
func gemRouteWrite(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gemRouteFacts reads the four settings routing touches; "" means absent.
func gemRouteFacts(settings, envPath string) map[string]string {
	base, _ := edit.GetEnvFile(envPath, "GOOGLE_GEMINI_BASE_URL")
	key, _ := edit.GetEnvFile(envPath, "GEMINI_API_KEY")
	auth, _ := edit.GetJSON(settings, "security.auth.selectedType")
	model, _ := edit.GetJSON(settings, "model.name")
	return map[string]string{"base": base, "key": key, "auth": auth, "model": model}
}

func gemRouteCheck(t *testing.T, got map[string]string, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func gemRouteSet(t *testing.T, a *Agent, key, v string) {
	t.Helper()
	if err := a.Field(key).Set(v); err != nil {
		t.Fatalf("set %s=%q: %v", key, v, err)
	}
}

func TestGemRouteWritesGatewayConfig(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1")
	gemRouteSet(t, a, "model", "fake/m1")

	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": gateway.URL(), "key": gateway.Token, "auth": "gemini-api-key", "model": "fake/m1",
	})
	if got := a.Field("provider").Get(); got != "dial" {
		t.Errorf("auth shows %q, want dial", got)
	}
	if got := a.Field("model").Get(); got != "fake/m1" {
		t.Errorf("model shows %q, want fake/m1", got)
	}
}

func TestGemRouteRestoresGoogleLogin(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1")
	gemRouteWrite(t, settings, "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  },\n  \"model\": {\n    \"name\": \"gemini-2.5-pro\"\n  }\n}\n")
	gemRouteWrite(t, envPath, "FOO=bar\n")

	gemRouteSet(t, a, "model", "fake/m1")
	gemRouteSet(t, a, "model", "")

	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": "", "key": "", "auth": "oauth-personal", "model": "gemini-2.5-pro",
	})
	if v, _ := edit.GetEnvFile(envPath, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if got := a.Field("provider").Get(); got != "google" {
		t.Errorf("auth shows %q, want google", got)
	}
}

func TestGemRouteKeepsOwnKeyAndBaseAcrossCatalogSwitch(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1", "m2")
	gemRouteWrite(t, settings, "{\"security\":{\"auth\":{\"selectedType\":\"gemini-api-key\"}},\"model\":{\"name\":\"gemini-2.5-flash\"}}\n")
	gemRouteWrite(t, envPath, "GOOGLE_GEMINI_BASE_URL=https://my.proxy.example/v1beta\nGEMINI_API_KEY=own-key\n")

	if got := a.Field("provider").Get(); got != "custom" {
		t.Fatalf("precondition: auth shows %q, want custom", got)
	}
	gemRouteSet(t, a, "model", "fake/m1")
	gemRouteSet(t, a, "model", "fake/m2") // already routed: must not overwrite the stash
	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": gateway.URL(), "key": gateway.Token, "auth": "gemini-api-key", "model": "fake/m2",
	})

	gemRouteSet(t, a, "model", "gemini-2.5-pro") // back to a Google model: original values, not the pick
	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": "https://my.proxy.example/v1beta", "key": "own-key", "auth": "gemini-api-key", "model": "gemini-2.5-flash",
	})
	if got := a.Field("provider").Get(); got != "custom" {
		t.Errorf("after switching back, auth shows %q, want custom", got)
	}
}

func TestGemRouteDefaultUnroutes(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1")
	gemRouteSet(t, a, "model", "fake/m1")
	gemRouteSet(t, a, "model", "") // "default"

	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": "", "key": "", "auth": "", "model": "",
	})
	if got := a.Field("provider").Get(); got != "google" {
		t.Errorf("auth shows %q after default, want google", got)
	}
}

func TestGemRouteRestoresApiKeyWhenAuthChosen(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1")
	gemRouteWrite(t, envPath, "GEMINI_API_KEY=own-key\n")
	gemRouteWrite(t, settings, "{\"security\":{\"auth\":{\"selectedType\":\"gemini-api-key\"}}}\n")

	gemRouteSet(t, a, "model", "fake/m1")
	gemRouteSet(t, a, "provider", "google")

	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": "", "key": "own-key", "auth": "oauth-personal", "model": "",
	})
}

func TestGemRouteManualDialNeedsRouting(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1")
	if err := a.Field("provider").Set("dial"); err == nil {
		t.Fatal("choosing dial without routing should fail")
	}
	if _, err := os.Stat(envPath); err == nil {
		t.Errorf("failed dial choice wrote %s", envPath)
	}
	if _, err := os.Stat(settings); err == nil {
		t.Errorf("failed dial choice wrote %s", settings)
	}

	gemRouteSet(t, a, "model", "fake/m1")
	if err := a.Field("provider").Set("dial"); err != nil {
		t.Fatalf("choosing dial while routed: %v", err)
	}
}

func TestGemRouteOptionsGrouped(t *testing.T) {
	a, _, _ := gemRouteEnv(t, "m1")
	groups := map[string]string{}
	for _, o := range a.Field("model").Options(nil) {
		groups[o.Value] = o.Group
	}
	if groups["gemini-2.5-pro"] != "Google Gemini" {
		t.Errorf("native model group = %q", groups["gemini-2.5-pro"])
	}
	if groups["fake/m1"] != "Fake" {
		t.Errorf("catalog model group = %q", groups["fake/m1"])
	}
}

func TestGemRouteNativePickWhileRoutedRestoresAbsent(t *testing.T) {
	a, settings, envPath := gemRouteEnv(t, "m1")
	gemRouteSet(t, a, "model", "fake/m1")
	gemRouteSet(t, a, "model", "gemini-2.5-pro") // routed, Google model: nothing was set before, so all absent

	gemRouteCheck(t, gemRouteFacts(settings, envPath), map[string]string{
		"base": "", "key": "", "auth": "", "model": "",
	})
	if got := a.Field("provider").Get(); got != "google" {
		t.Errorf("auth shows %q, want google", got)
	}
}
