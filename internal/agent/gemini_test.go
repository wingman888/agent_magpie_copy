package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// geminiHome points HOME and the dial config dir at a fresh temp tree and
// returns the home directory. Nothing outside it is touched.
func geminiHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

// addFakeVendor saves an OpenAI-compatible vendor "fake" with model m1.
func addFakeVendor(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "http://127.0.0.1:9/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
}

func geminiFiles(t *testing.T, home string) (settings, env string) {
	t.Helper()
	return filepath.Join(home, ".gemini", "settings.json"), filepath.Join(home, ".gemini", ".env")
}

func writeTest(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func envGet(t *testing.T, env, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(env, key)
}

func jsonAt(t *testing.T, settings, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(settings, key)
}

func field(t *testing.T, a *Agent, key string) *Field {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	return f
}

func TestGeminiRouteToCatalogModel(t *testing.T) {
	home := geminiHome(t)
	addFakeVendor(t)
	settings, env := geminiFiles(t, home)
	writeTest(t, env, "FOO=bar\n")
	writeTest(t, settings, "{\n  \"other\": 1,\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n")
	a := gemini(home)

	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := envGet(t, env, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url = %q, want %q (no /v1beta)", v, gateway.URL())
	}
	if v, _ := envGet(t, env, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want gateway token", v)
	}
	if v, _ := envGet(t, env, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if v, _ := jsonAt(t, settings, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q", v)
	}
	if v, _ := jsonAt(t, settings, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", v)
	}
	if v, _ := jsonAt(t, settings, "other"); v != "1" {
		t.Errorf("unrelated settings field lost: other=%q", v)
	}
	if got := field(t, a, "auth").Get(); got != "dial" {
		t.Errorf("auth Get = %q, want dial", got)
	}
	if got := field(t, a, "provider").Get(); got != "Fake" {
		t.Errorf("provider Get = %q, want Fake", got)
	}
	// switching between catalog models keeps the original values stashed
	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := field(t, a, "model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if v, _ := jsonAt(t, settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("native pick: selectedType = %q, want oauth-personal restored", v)
	}
	if _, ok := envGet(t, env, "GEMINI_API_KEY"); ok {
		t.Errorf("native pick: GEMINI_API_KEY should be removed (none before routing)")
	}
	if _, ok := envGet(t, env, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("native pick: GOOGLE_GEMINI_BASE_URL should be removed")
	}
	if v, _ := jsonAt(t, settings, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("native pick: model.name = %q, want gemini-2.5-pro", v)
	}
	if v, _ := envGet(t, env, "FOO"); v != "bar" {
		t.Errorf("native pick: FOO lost")
	}
	if v, _ := jsonAt(t, settings, "other"); v != "1" {
		t.Errorf("native pick: other lost")
	}
	if got := field(t, a, "auth").Get(); got != "google" {
		t.Errorf("after native pick auth = %q, want original (oauth-personal → google)", got)
	}
}

func TestGeminiNativeRestoresPriorKeyAndBase(t *testing.T) {
	home := geminiHome(t)
	addFakeVendor(t)
	settings, env := geminiFiles(t, home)
	writeTest(t, env, "GEMINI_API_KEY=mine\nGOOGLE_GEMINI_BASE_URL=https://custom.example\n")
	writeTest(t, settings, "{\"security\":{\"auth\":{\"selectedType\":\"vertex-ai\"}},\"model\":{\"name\":\"old\"}}\n")
	a := gemini(home)

	if got := field(t, a, "auth").Get(); got != "custom" {
		t.Fatalf("auth before routing = %q, want custom", got)
	}
	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := envGet(t, env, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatalf("routed key = %q", v)
	}
	if err := field(t, a, "model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := envGet(t, env, "GEMINI_API_KEY"); v != "mine" {
		t.Errorf("GEMINI_API_KEY = %q, want mine restored", v)
	}
	if v, _ := envGet(t, env, "GOOGLE_GEMINI_BASE_URL"); v != "https://custom.example" {
		t.Errorf("base = %q, want custom restored", v)
	}
	if v, _ := jsonAt(t, settings, "security.auth.selectedType"); v != "vertex-ai" {
		t.Errorf("selectedType = %q, want vertex-ai restored", v)
	}
	if v, _ := jsonAt(t, settings, "model.name"); v != "gemini-2.5-flash" {
		t.Errorf("model.name = %q, want the native pick, not the old one", v)
	}
}

func TestGeminiDefaultRestores(t *testing.T) {
	home := geminiHome(t)
	addFakeVendor(t)
	settings, env := geminiFiles(t, home)
	writeTest(t, env, "FOO=bar\n")
	writeTest(t, settings, "{\"other\":true}\n")
	a := gemini(home)

	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := field(t, a, "model").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := envGet(t, env, "GEMINI_API_KEY"); ok {
		t.Errorf("Default: GEMINI_API_KEY should be gone when there was no key before")
	}
	if _, ok := envGet(t, env, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("Default: base URL should be gone")
	}
	if _, ok := jsonAt(t, settings, "security.auth.selectedType"); ok {
		t.Errorf("Default: selectedType should be gone")
	}
	if _, ok := jsonAt(t, settings, "model.name"); ok {
		t.Errorf("Default: model.name should be removed")
	}
	if v, _ := envGet(t, env, "FOO"); v != "bar" {
		t.Errorf("Default: FOO lost")
	}
	if v, _ := jsonAt(t, settings, "other"); v != "true" {
		t.Errorf("Default: other lost")
	}
	if got := field(t, a, "auth").Get(); got == "dial" {
		t.Errorf("auth still dial after Default")
	}
}

func TestGeminiAuthDialRules(t *testing.T) {
	home := geminiHome(t)
	addFakeVendor(t)
	settings, env := geminiFiles(t, home)
	a := gemini(home)
	auth := field(t, a, "auth")

	if err := auth.Set("dial"); err == nil {
		t.Errorf("Set(dial) while not routed should fail")
	}
	if _, ok := jsonAt(t, settings, "security.auth.selectedType"); ok {
		t.Errorf("failed Set(dial) must not change anything")
	}

	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	before := readTest(t, settings) + "\n---\n" + readTest(t, env)
	if err := auth.Set("dial"); err != nil {
		t.Fatalf("Set(dial) while routed: %v", err)
	}
	if after := readTest(t, settings) + "\n---\n" + readTest(t, env); after != before {
		t.Errorf("Set(dial) while routed changed files:\n%s\n---\n%s", before, after)
	}
	if got := auth.Get(); got != "dial" {
		t.Errorf("auth Get = %q, want dial", got)
	}
	// the auth option list only offers dial while routed
	found := false
	for _, o := range auth.Options(nil) {
		if o.Value == "dial" {
			found = true
		}
	}
	if !found {
		t.Errorf("auth options should include dial while routed")
	}
}

func TestGeminiFieldLookupByLabel(t *testing.T) {
	home := geminiHome(t)
	a := gemini(home)
	if f := a.Field("auth"); f == nil || f.Key != "auth" {
		t.Errorf("Field(auth) = %+v", f)
	}
	if f := a.Field("Auth"); f == nil || f.Key != "auth" {
		t.Errorf("Field(Auth) should match the label case-insensitively")
	}
	if f := a.Field("provider"); f == nil {
		t.Errorf("no provider field")
	}
	// pi shows its effort field as "thinking": that label must find it
	if f := (pi(home)).Field("thinking"); f == nil || f.Key != "effort" {
		t.Errorf("Field(thinking) on pi should find the effort field")
	}
	if f := a.Field("nope"); f != nil {
		t.Errorf("unknown field should be nil")
	}
}

func TestGeminiAccountNoteAndCodexFilter(t *testing.T) {
	home := geminiHome(t)
	addFakeVendor(t)
	writeTest(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"chatgpt","tokens":{`+
		`"id_token":"`+fakeJWT(map[string]any{"email": "me@example.com"})+`",`+
		`"access_token":"`+fakeJWT(map[string]any{"exp": 4102444800})+`",`+
		`"refresh_token":"r","account_id":"acc"}}`)

	a := gemini(home)
	var sawCodex, sawFake bool
	for _, o := range field(t, a, "model").Options(nil) {
		if o.Value == "codex/gpt-5.5" {
			sawCodex = true
			if o.Note != "me@example.com · via dial" {
				t.Errorf("codex note = %q, want the account user", o.Note)
			}
		}
		if o.Value == "fake/m1" {
			sawFake = true
		}
	}
	if !sawCodex || !sawFake {
		t.Errorf("gemini model options: codex=%v fake=%v", sawCodex, sawFake)
	}

	for _, o := range codex(home).Field("model").Options(nil) {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Errorf("codex's own picker must not list %q", o.Value)
		}
	}
}

// fakeJWT builds an unsigned token with the given payload.
func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}
