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

const geminiOrigSettings = `{
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`

// geminiSetup points HOME and dial's config at temp dirs, writes the given
// Gemini CLI files ("" leaves a file absent) and adds a provider "fake" with
// two models to the catalog.
func geminiSetup(t *testing.T, env, settings string) (a *Agent, envPath, settingsPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Models: []string{"m1", "m2"}, Chat: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	envPath = filepath.Join(a.Dir, ".env")
	settingsPath = a.Path
	if env != "" {
		if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if settings != "" {
		if err := os.WriteFile(settingsPath, []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return a, envPath, settingsPath
}

// envIs asserts a dotenv key's value; "" means the key must be absent.
func envIs(t *testing.T, path, key, want string) {
	t.Helper()
	got, ok := edit.GetEnvFile(path, key)
	if want == "" {
		if ok {
			t.Errorf("%s = %q, want it removed", key, got)
		}
		return
	}
	if got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

// jsonIs asserts a settings value; "" means the key must be absent.
func jsonIs(t *testing.T, path, key, want string) {
	t.Helper()
	got, ok := edit.GetJSON(path, key)
	if want == "" {
		if ok {
			t.Errorf("%s = %q, want it removed", key, got)
		}
		return
	}
	if got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func TestGeminiRouteWritesGatewayConfig(t *testing.T) {
	a, envPath, sp := geminiSetup(t, "GEMINI_API_KEY=mine\n", geminiOrigSettings)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	envIs(t, envPath, "GOOGLE_GEMINI_BASE_URL", gateway.URL())
	envIs(t, envPath, "GEMINI_API_KEY", gateway.Token)
	jsonIs(t, sp, "security.auth.selectedType", "gemini-api-key")
	jsonIs(t, sp, "model.name", "fake/m1")
	if got := a.Field("provider").Get(); got != dialID {
		t.Errorf("auth shows %q, want %q", got, dialID)
	}
}

func TestGeminiRoutedOptionsListDial(t *testing.T) {
	a, _, _ := geminiSetup(t, "", geminiOrigSettings)
	hasDial := func() bool {
		for _, o := range a.Field("provider").Options(nil) {
			if o.Value == dialID {
				return true
			}
		}
		return false
	}
	if hasDial() {
		t.Fatal("dial auth offered before routing")
	}
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if !hasDial() {
		t.Error("dial auth not offered while routed")
	}
}

func TestGeminiNativeModelRestoresOriginals(t *testing.T) {
	a, envPath, sp := geminiSetup(t, "GEMINI_API_KEY=mine\n", geminiOrigSettings)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	// switching between catalog models must not overwrite the stash
	if err := a.Field("model").Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	envIs(t, envPath, "GOOGLE_GEMINI_BASE_URL", gateway.URL())
	jsonIs(t, sp, "model.name", "fake/m2")

	if err := a.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	envIs(t, envPath, "GOOGLE_GEMINI_BASE_URL", "")
	envIs(t, envPath, "GEMINI_API_KEY", "mine")
	jsonIs(t, sp, "security.auth.selectedType", "oauth-personal")
	jsonIs(t, sp, "model.name", "gemini-2.5-flash")
	if got := a.Field("provider").Get(); got != "google" {
		t.Errorf("auth shows %q after restore, want google", got)
	}
}

func TestGeminiDefaultRestoresOriginals(t *testing.T) {
	a, envPath, sp := geminiSetup(t, "GEMINI_API_KEY=mine\nGOOGLE_GEMINI_BASE_URL=https://relay.example\n",
		geminiOrigSettings)
	// a custom base URL is the user's own; routing must give it back
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("provider").Set(""); err != nil { // dial gemini default
		t.Fatal(err)
	}
	envIs(t, envPath, "GOOGLE_GEMINI_BASE_URL", "https://relay.example")
	envIs(t, envPath, "GEMINI_API_KEY", "mine")
	jsonIs(t, sp, "security.auth.selectedType", "oauth-personal")
	jsonIs(t, sp, "model.name", "gemini-2.5-pro")
}

func TestGeminiAbsentValuesAreRemoved(t *testing.T) {
	a, envPath, sp := geminiSetup(t, "", "{}\n")
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil { // default
		t.Fatal(err)
	}
	envIs(t, envPath, "GOOGLE_GEMINI_BASE_URL", "")
	envIs(t, envPath, "GEMINI_API_KEY", "")
	jsonIs(t, sp, "security.auth.selectedType", "")
	jsonIs(t, sp, "model.name", "")
}

func TestGeminiAuthChangeUnroutes(t *testing.T) {
	a, envPath, sp := geminiSetup(t, "GEMINI_API_KEY=mine\n", geminiOrigSettings)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("provider").Set("google"); err != nil {
		t.Fatal(err)
	}
	envIs(t, envPath, "GOOGLE_GEMINI_BASE_URL", "")
	envIs(t, envPath, "GEMINI_API_KEY", "mine")
	jsonIs(t, sp, "security.auth.selectedType", "oauth-personal")
}

func TestGeminiManualDialNeedsRoute(t *testing.T) {
	a, _, sp := geminiSetup(t, "", geminiOrigSettings)
	err := a.Field("provider").Set(dialID)
	if err == nil {
		t.Fatal("selecting dial without routing should fail")
	}
	if !strings.Contains(err.Error(), "dial gemini") {
		t.Errorf("error gives no guidance: %v", err)
	}
	jsonIs(t, sp, "security.auth.selectedType", "oauth-personal")
}

func TestGeminiFieldByDisplayName(t *testing.T) {
	a, _, sp := geminiSetup(t, "", geminiOrigSettings)
	if f := a.Field("auth"); f == nil || f.Key != "provider" {
		t.Fatalf(`Field("auth") = %v, want the provider field`, f)
	}
	if f := a.Field("provider"); f == nil || f.Label != "auth" {
		t.Fatalf(`Field("provider") = %v, want the provider field`, f)
	}
	if f := a.Field("model"); f == nil || f.Key != "model" {
		t.Fatalf(`Field("model") = %v, want the model field`, f)
	}
	if a.Field("nope") != nil {
		t.Error("unknown name matched a field")
	}
	if err := a.Field("auth").Set("vertex"); err != nil {
		t.Fatal(err)
	}
	jsonIs(t, sp, "security.auth.selectedType", "vertex-ai")
}

func TestGeminiModelOptionsGrouped(t *testing.T) {
	a, _, _ := geminiSetup(t, "", geminiOrigSettings)
	groups := map[string]bool{}
	for _, o := range a.Field("model").Options(nil) {
		groups[o.Group] = true
	}
	if !groups["Gemini CLI"] {
		t.Error("native Gemini models are not in their own group")
	}
	if !groups["Fake"] {
		t.Error("catalog models are not grouped by provider")
	}
}
