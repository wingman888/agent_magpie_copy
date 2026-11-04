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

// geminiTestHome points HOME and the XDG dirs at a temp dir and saves one
// ready provider, "fake", whose models show up in the catalog as fake/m1 and
// fake/m2.
func geminiTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "sk-fake",
		Chat: "https://fake.example/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	return home
}

func geminiWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func geminiEnv(t *testing.T, path, key string) string {
	t.Helper()
	v, _ := edit.GetEnvFile(path, key)
	return v
}

func geminiJSON(t *testing.T, path, key string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, key)
	return v
}

// geminiOwnConfig is a user with their own key, a custom base URL and a model.
func geminiOwnConfig(t *testing.T, dir string) {
	t.Helper()
	geminiWrite(t, filepath.Join(dir, ".env"), "GOOGLE_GEMINI_BASE_URL=https://relay.example/gemini\nGEMINI_API_KEY=own-key\n")
	geminiWrite(t, filepath.Join(dir, "settings.json"),
		`{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`)
}

func TestGeminiRouteAndBackRestoresOwnConfig(t *testing.T) {
	home := geminiTestHome(t)
	dir := filepath.Join(home, ".gemini")
	envPath := filepath.Join(dir, ".env")
	path := filepath.Join(dir, "settings.json")
	geminiOwnConfig(t, dir)
	a := gemini(home)
	model, auth := a.Field("model"), a.Field("provider")

	// a catalog model routes everything through the gateway
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if got := geminiEnv(t, envPath, "GOOGLE_GEMINI_BASE_URL"); got != gateway.URL() {
		t.Errorf("base URL = %q, want %q", got, gateway.URL())
	}
	if got := geminiEnv(t, envPath, "GEMINI_API_KEY"); got != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want %q", got, gateway.Token)
	}
	if got := geminiJSON(t, path, "security.auth.selectedType"); got != "gemini-api-key" {
		t.Errorf("selectedType = %q", got)
	}
	if got := geminiJSON(t, path, "model.name"); got != "fake/m1" {
		t.Errorf("model.name = %q", got)
	}
	if got := auth.Get(); got != dialID {
		t.Errorf("auth shows %q, want %q", got, dialID)
	}

	// another catalog model while routed must not overwrite the originals
	if err := model.Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if got := geminiJSON(t, path, "model.name"); got != "fake/m2" {
		t.Errorf("model.name = %q, want fake/m2", got)
	}

	// back to a Google model: the original base, key, auth type come back
	if err := model.Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if got := geminiEnv(t, envPath, "GOOGLE_GEMINI_BASE_URL"); got != "https://relay.example/gemini" {
		t.Errorf("base URL = %q, want the original", got)
	}
	if got := geminiEnv(t, envPath, "GEMINI_API_KEY"); got != "own-key" {
		t.Errorf("GEMINI_API_KEY = %q, want own-key", got)
	}
	if got := geminiJSON(t, path, "security.auth.selectedType"); got != "gemini-api-key" {
		t.Errorf("selectedType = %q", got)
	}
	if got := geminiJSON(t, path, "model.name"); got != "gemini-2.5-flash" {
		t.Errorf("model.name = %q, want the native pick", got)
	}
	if got := auth.Get(); got != "custom" {
		t.Errorf("auth shows %q, want custom (the original base URL)", got)
	}
}

func TestGeminiDefaultModelUndoesRouteExactly(t *testing.T) {
	home := geminiTestHome(t)
	dir := filepath.Join(home, ".gemini")
	envPath := filepath.Join(dir, ".env")
	path := filepath.Join(dir, "settings.json")
	geminiOwnConfig(t, dir)
	a := gemini(home)

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := geminiEnv(t, envPath, "GOOGLE_GEMINI_BASE_URL"); got != "https://relay.example/gemini" {
		t.Errorf("base URL = %q, want the original", got)
	}
	if got := geminiEnv(t, envPath, "GEMINI_API_KEY"); got != "own-key" {
		t.Errorf("GEMINI_API_KEY = %q, want own-key", got)
	}
	if got := geminiJSON(t, path, "model.name"); got != "gemini-2.5-pro" {
		t.Errorf("model.name = %q, want gemini-2.5-pro", got)
	}
}

func TestGeminiRouteRemovesThingsThatWereAbsent(t *testing.T) {
	home := geminiTestHome(t)
	dir := filepath.Join(home, ".gemini")
	envPath := filepath.Join(dir, ".env")
	path := filepath.Join(dir, "settings.json")
	a := gemini(home)

	// a Google sign-in user: no .env, no model, no key
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := geminiEnv(t, envPath, "GOOGLE_GEMINI_BASE_URL"); got != "" {
		t.Errorf("base URL = %q, want it removed", got)
	}
	if got := geminiEnv(t, envPath, "GEMINI_API_KEY"); got != "" {
		t.Errorf("GEMINI_API_KEY = %q, want it removed", got)
	}
	if got := geminiJSON(t, path, "security.auth.selectedType"); got != "" {
		t.Errorf("selectedType = %q, want it removed", got)
	}
	if got := geminiJSON(t, path, "model.name"); got != "" {
		t.Errorf("model.name = %q, want it removed", got)
	}
	if got := a.Field("provider").Get(); got != "google" {
		t.Errorf("auth shows %q, want google", got)
	}
}

func TestGeminiDialAuthNeedsRoute(t *testing.T) {
	home := geminiTestHome(t)
	a := gemini(home)
	auth := a.Field("provider")

	err := auth.Set(dialID)
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("dial auth without a route: err = %v, want guidance to pick a model", err)
	}

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := auth.Set(dialID); err != nil {
		t.Errorf("dial auth while routed: %v", err)
	}
	if got := auth.Get(); got != dialID {
		t.Errorf("auth shows %q, want %q", got, dialID)
	}
}

func TestGeminiAuthDefaultLeavesRoute(t *testing.T) {
	home := geminiTestHome(t)
	dir := filepath.Join(home, ".gemini")
	envPath := filepath.Join(dir, ".env")
	path := filepath.Join(dir, "settings.json")
	geminiOwnConfig(t, dir)
	a := gemini(home)

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if got := geminiEnv(t, envPath, "GOOGLE_GEMINI_BASE_URL"); got != "https://relay.example/gemini" {
		t.Errorf("base URL = %q, want the original", got)
	}
	if got := geminiEnv(t, envPath, "GEMINI_API_KEY"); got != "own-key" {
		t.Errorf("GEMINI_API_KEY = %q, want own-key", got)
	}
	if got := geminiJSON(t, path, "model.name"); got != "gemini-2.5-pro" {
		t.Errorf("model.name = %q, want gemini-2.5-pro", got)
	}
}

func TestGeminiPickerGroupsAndDialOption(t *testing.T) {
	home := geminiTestHome(t)
	a := gemini(home)

	var hasDialAuth bool
	for _, o := range a.Field("provider").Options(nil) {
		if o.Value == dialID {
			hasDialAuth = true
		}
	}
	if !hasDialAuth {
		t.Error("auth options lack the dial entry")
	}

	var native, catalogModel bool
	for _, o := range a.Field("model").Options(nil) {
		if o.Group == "Gemini CLI" && strings.HasPrefix(o.Value, "gemini-") {
			native = true
		}
		if o.Value == "fake/m1" && o.Group == "Fake" {
			catalogModel = true
		}
	}
	if !native || !catalogModel {
		t.Errorf("model options: native group %v, catalog group %v", native, catalogModel)
	}
}

func TestGeminiFieldByDisplayName(t *testing.T) {
	home := geminiTestHome(t)
	t.Setenv("HOME", home)
	a, err := Find("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if f := a.Field("auth"); f == nil || f.Key != "provider" {
		t.Errorf(`Field("auth") = %+v, want the provider field`, f)
	}
	if f := a.Field("AUTH"); f == nil || f.Key != "provider" {
		t.Errorf(`Field("AUTH") = %+v, want the provider field`, f)
	}
	if a.Field("provider") != a.Field("auth") {
		t.Error("the key and the label should find the same field")
	}
	if f := a.Field("model"); f == nil || f.Key != "model" {
		t.Errorf(`Field("model") = %+v`, f)
	}
	if a.Field("nope") != nil {
		t.Error("an unknown name should find nothing")
	}
}

// Leaving the gateway for Google sign-in must put the user's own base URL
// back and keep it; the switch to a native auth type does not remove it.
func TestGeminiLeaveRouteForGoogleKeepsOwnBaseURL(t *testing.T) {
	home := geminiTestHome(t)
	dir := filepath.Join(home, ".gemini")
	envPath := filepath.Join(dir, ".env")
	geminiOwnConfig(t, dir)
	a := gemini(home)

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("provider").Set("google"); err != nil {
		t.Fatal(err)
	}
	if got := geminiEnv(t, envPath, "GOOGLE_GEMINI_BASE_URL"); got != "https://relay.example/gemini" {
		t.Errorf("base URL = %q, want the original relay", got)
	}
	if got := geminiEnv(t, envPath, "GEMINI_API_KEY"); got != "own-key" {
		t.Errorf("GEMINI_API_KEY = %q, want own-key", got)
	}
	if got := geminiJSON(t, filepath.Join(dir, "settings.json"), "security.auth.selectedType"); got != "oauth-personal" {
		t.Errorf("selectedType = %q, want oauth-personal", got)
	}
}
