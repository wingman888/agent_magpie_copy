package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// isolateGemini points HOME and the XDG dirs at a temp dir and writes one
// vendor, "fake", so the catalog has a dial model to route to.
func isolateGemini(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("DIAL_ADDR", "")
	writeAgentFile(t, filepath.Join(home, "xdg-config", "dial", "providers.json"),
		`{"providers":[{"id":"fake","name":"Fake","chat":"http://127.0.0.1:9/v1","key":"k","models":["m1"]}]}`)
	return home
}

func writeAgentFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAgentFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}

func geminiEnv(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(filepath.Join(home, ".gemini", ".env"), key)
}

func geminiSettings(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(filepath.Join(home, ".gemini", "settings.json"), key)
}

func setField(t *testing.T, a *Agent, key, value string) {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	if err := f.Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", key, value, err)
	}
}

func TestGeminiRouteCatalogModel(t *testing.T) {
	home := isolateGemini(t)
	dir := filepath.Join(home, ".gemini")
	writeAgentFile(t, filepath.Join(dir, ".env"), "FOO=bar\nGEMINI_API_KEY=AIza-mine\n")
	writeAgentFile(t, filepath.Join(dir, "settings.json"),
		`{"other":true,"security":{"auth":{"selectedType":"oauth-personal"}}}`)
	a := gemini(home)

	setField(t, a, "model", "fake/m1")

	if v, _ := geminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q, want %q", v, gateway.URL())
	}
	if v, _ := geminiEnv(t, home, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want %q", v, gateway.Token)
	}
	if got := readAgentFile(t, filepath.Join(dir, ".env")); !strings.Contains(got, "FOO=bar\n") {
		t.Errorf(".env lost unrelated line: %q", got)
	}
	if v, _ := geminiSettings(t, home, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q", v)
	}
	if v, _ := geminiSettings(t, home, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", v)
	}
	if v, _ := geminiSettings(t, home, "other"); v != "true" {
		t.Errorf("unrelated settings field lost: other = %q", v)
	}
	if got := a.Values()["auth"]; got != "dial" {
		t.Errorf("auth = %q, want dial", got)
	}
	if got := a.Values()["provider"]; got != "fake" {
		t.Errorf("provider = %q, want fake", got)
	}

	// picking a native model restores what was there before routing
	setField(t, a, "model", "gemini-2.5-pro")
	if v, _ := geminiSettings(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType after native = %q, want oauth-personal", v)
	}
	if v, _ := geminiEnv(t, home, "GEMINI_API_KEY"); v != "AIza-mine" {
		t.Errorf("GEMINI_API_KEY after native = %q, want AIza-mine", v)
	}
	if _, ok := geminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL should be removed after native")
	}
	if v, _ := geminiSettings(t, home, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("model.name after native = %q, want gemini-2.5-pro (not the old routed one)", v)
	}
	if got := a.Values()["auth"]; got == "dial" {
		t.Errorf("auth still dial after native pick")
	}
	if got := readAgentFile(t, filepath.Join(dir, ".env")); !strings.Contains(got, "FOO=bar") {
		t.Errorf(".env lost unrelated line after restore: %q", got)
	}
}

func TestGeminiDefaultRestoresAndRemovesAbsent(t *testing.T) {
	home := isolateGemini(t)
	dir := filepath.Join(home, ".gemini")
	writeAgentFile(t, filepath.Join(dir, ".env"), "FOO=bar\n")
	writeAgentFile(t, filepath.Join(dir, "settings.json"), `{"other":true}`)
	a := gemini(home)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "")

	if _, ok := geminiEnv(t, home, "GEMINI_API_KEY"); ok {
		t.Errorf("GEMINI_API_KEY should be removed: none was there before routing")
	}
	if _, ok := geminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL should be removed")
	}
	if _, ok := geminiSettings(t, home, "security.auth.selectedType"); ok {
		t.Errorf("selectedType should be removed")
	}
	if _, ok := geminiSettings(t, home, "model.name"); ok {
		t.Errorf("model.name should be removed on Default")
	}
	if v, _ := geminiSettings(t, home, "other"); v != "true" {
		t.Errorf("unrelated settings field lost")
	}
	if got := readAgentFile(t, filepath.Join(dir, ".env")); !strings.Contains(got, "FOO=bar") {
		t.Errorf(".env lost unrelated line: %q", got)
	}
}

func TestGeminiDefaultKeepsPriorKey(t *testing.T) {
	home := isolateGemini(t)
	dir := filepath.Join(home, ".gemini")
	writeAgentFile(t, filepath.Join(dir, ".env"), "GEMINI_API_KEY=AIza-mine\n")
	writeAgentFile(t, filepath.Join(dir, "settings.json"), `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`)
	a := gemini(home)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "")

	if v, _ := geminiEnv(t, home, "GEMINI_API_KEY"); v != "AIza-mine" {
		t.Errorf("GEMINI_API_KEY = %q, want the key from before routing", v)
	}
	if v, _ := geminiSettings(t, home, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key restored", v)
	}
	if _, ok := geminiSettings(t, home, "model.name"); ok {
		t.Errorf("model.name should be removed on Default")
	}
}

func TestGeminiAuthGetSetRoutedAndUnrouted(t *testing.T) {
	home := isolateGemini(t)
	a := gemini(home)

	if got := a.Values()["auth"]; got != "google" {
		t.Errorf("unrouted auth = %q, want google", got)
	}
	if err := a.Field("auth").Set("dial"); err == nil {
		t.Errorf("Set(dial) while unrouted should fail")
	}
	if _, ok := geminiSettings(t, home, "security.auth.selectedType"); ok {
		t.Errorf("failed Set(dial) must not write settings")
	}

	setField(t, a, "model", "fake/m1")
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Fatalf("Set(dial) while routed: %v", err)
	}
	if got := a.Values()["auth"]; got != "dial" {
		t.Errorf("routed auth = %q, want dial", got)
	}
	if v, _ := geminiSettings(t, home, "model.name"); v != "fake/m1" {
		t.Errorf("Set(dial) changed the model to %q", v)
	}
	if v, _ := geminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("Set(dial) changed the base URL to %q", v)
	}
}

func TestGeminiFieldLookupByKeyAndLabel(t *testing.T) {
	home := isolateGemini(t)
	a := gemini(home)
	for _, q := range []string{"auth", "Auth"} {
		if f := a.Field(q); f == nil || f.Key != "auth" {
			t.Errorf("Field(%q) did not find the auth field", q)
		}
	}
	if f := a.Field("provider"); f == nil || f.Key != "provider" {
		t.Errorf("Field(provider) did not find the provider field")
	}

	// a field whose key differs from its label: crush's large model
	c := crush(home, filepath.Join(home, "cfg"))
	if f := c.Field("large"); f == nil || f.Key != "model" {
		t.Errorf("crush Field(large) should find the model field by label")
	}
	if f := c.Field("model"); f == nil || f.Key != "model" {
		t.Errorf("crush Field(model) should still find it by key")
	}
}

func TestGeminiProviderFieldSelectsCatalog(t *testing.T) {
	home := isolateGemini(t)
	a := gemini(home)

	setField(t, a, "provider", "fake")
	if v, _ := geminiSettings(t, home, "model.name"); v != "fake/m1" {
		t.Errorf("provider=fake set model.name = %q, want fake/m1", v)
	}
	if err := a.Field("provider").Set("nope"); err == nil {
		t.Errorf("unknown provider should fail")
	}
	opts := a.Field("provider").Options(nil)
	found := false
	for _, o := range opts {
		if o.Value == "fake" {
			found = true
		}
		if o.Value == "google" {
			t.Errorf("google should not be offered as a provider: native models cover it")
		}
	}
	if !found {
		t.Errorf("provider options lack fake: %+v", opts)
	}
}

func TestGeminiModelFieldJSONRoundTrip(t *testing.T) {
	home := isolateGemini(t)
	dir := filepath.Join(home, ".gemini")
	writeAgentFile(t, filepath.Join(dir, "settings.json"), `{"model":{"name":"gemini-2.5-flash"}}`)
	a := gemini(home)
	if got := a.Values()["model"]; got != "gemini-2.5-flash" {
		t.Fatalf("model = %q", got)
	}
	setField(t, a, "model", "fake/m1")
	var s struct {
		Model struct {
			Name string `json:"name"`
		} `json:"model"`
	}
	b := readAgentFile(t, filepath.Join(dir, "settings.json"))
	if err := json.Unmarshal([]byte(b), &s); err != nil || s.Model.Name != "fake/m1" {
		t.Fatalf("settings.json = %q (err %v)", b, err)
	}
}
