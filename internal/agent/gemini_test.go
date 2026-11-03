package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// isolateGemini points HOME and dial's config and cache at a fresh temp tree.
func isolateGemini(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	return home
}

func writeGeminiFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// saveFakeProvider adds a key-bearing provider "fake" with two models.
func saveFakeProvider(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "http://127.0.0.1:9/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
}

// envVars parses a dotenv file the simple way: KEY=value per line.
func envVars(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}
	return out
}

// settingsAt reads settings.json and returns the value at a dotted path.
func settingsAt(t *testing.T, path, dotted string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var cur any = m
	for _, part := range strings.Split(dotted, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = obj[part]; !ok {
			return "", false
		}
	}
	s, ok := cur.(string)
	return s, ok
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}

func TestGeminiRoutesCatalogModelAndRestoresNative(t *testing.T) {
	home := isolateGemini(t)
	saveFakeProvider(t)
	dir := filepath.Join(home, ".gemini")
	env := filepath.Join(dir, ".env")
	settings := filepath.Join(dir, "settings.json")
	writeGeminiFile(t, env, "FOO=bar\nGEMINI_API_KEY=oldkey\nGOOGLE_GEMINI_BASE_URL=https://old.example/\n")
	writeGeminiFile(t, settings, `{"other":"x","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)

	g := gemini(home)
	if err := g.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	ev := envVars(t, env)
	if ev["GOOGLE_GEMINI_BASE_URL"] != gateway.URL() {
		t.Errorf("GOOGLE_GEMINI_BASE_URL = %q, want %q", ev["GOOGLE_GEMINI_BASE_URL"], gateway.URL())
	}
	if ev["GEMINI_API_KEY"] != gateway.Token {
		t.Errorf("GEMINI_API_KEY = %q, want %q", ev["GEMINI_API_KEY"], gateway.Token)
	}
	if ev["FOO"] != "bar" {
		t.Errorf("unrelated .env line lost: FOO = %q", ev["FOO"])
	}
	if v, _ := settingsAt(t, settings, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", v)
	}
	if v, _ := settingsAt(t, settings, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", v)
	}
	if v, _ := settingsAt(t, settings, "other"); v != "x" {
		t.Errorf("unrelated settings field lost: other = %q", v)
	}
	if got := g.Field("auth").Get(); got != "dial" {
		t.Errorf("auth while routed = %q, want dial", got)
	}
	if got := g.Field("provider").Get(); got != "fake" {
		t.Errorf("provider while routed = %q, want fake", got)
	}

	// picking a native model puts the stashed auth, key and base back
	if err := g.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	ev = envVars(t, env)
	if ev["GEMINI_API_KEY"] != "oldkey" {
		t.Errorf("GEMINI_API_KEY not restored: %q", ev["GEMINI_API_KEY"])
	}
	if ev["GOOGLE_GEMINI_BASE_URL"] != "https://old.example/" {
		t.Errorf("GOOGLE_GEMINI_BASE_URL not restored: %q", ev["GOOGLE_GEMINI_BASE_URL"])
	}
	if ev["FOO"] != "bar" {
		t.Errorf("unrelated .env line lost after restore: FOO = %q", ev["FOO"])
	}
	if v, _ := settingsAt(t, settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType not restored: %q", v)
	}
	if v, _ := settingsAt(t, settings, "model.name"); v != "gemini-2.5-flash" {
		t.Errorf("model.name = %q, want the native pick gemini-2.5-flash", v)
	}
	if got := g.Field("auth").Get(); got != "custom" {
		t.Errorf("auth after restore = %q, want custom (the old base URL is back)", got)
	}
}

func TestGeminiDefaultDropsWhatWasAbsent(t *testing.T) {
	home := isolateGemini(t)
	saveFakeProvider(t)
	dir := filepath.Join(home, ".gemini")
	env := filepath.Join(dir, ".env")
	settings := filepath.Join(dir, "settings.json")
	writeGeminiFile(t, env, "FOO=bar\n")
	writeGeminiFile(t, settings, `{"other":"x"}`)

	g := gemini(home)
	if err := g.Field("model").Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if err := g.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	ev := envVars(t, env)
	for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL"} {
		if hasKey(ev, k) {
			t.Errorf("%s should be gone after Default, got %q", k, ev[k])
		}
	}
	if ev["FOO"] != "bar" {
		t.Errorf("unrelated .env line lost: FOO = %q", ev["FOO"])
	}
	if _, ok := settingsAt(t, settings, "security.auth.selectedType"); ok {
		t.Error("selectedType should be removed after Default")
	}
	if _, ok := settingsAt(t, settings, "model.name"); ok {
		t.Error("model.name should be removed after Default")
	}
	if v, _ := settingsAt(t, settings, "other"); v != "x" {
		t.Errorf("unrelated settings field lost: other = %q", v)
	}
	if got := g.Field("auth").Get(); got != "google" {
		t.Errorf("auth after Default = %q, want google", got)
	}
}

func TestGeminiDefaultOnAuthRestoresKeyless(t *testing.T) {
	home := isolateGemini(t)
	saveFakeProvider(t)
	env := filepath.Join(home, ".gemini", ".env")
	settings := filepath.Join(home, ".gemini", "settings.json")
	writeGeminiFile(t, env, "FOO=bar\n")
	writeGeminiFile(t, settings, `{}`)

	g := gemini(home)
	if err := g.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	// `dial gemini default` sets the first field, auth, to ""
	if err := g.Field("auth").Set(""); err != nil {
		t.Fatal(err)
	}
	if hasKey(envVars(t, env), "GEMINI_API_KEY") {
		t.Error("GEMINI_API_KEY should not remain when none was there before routing")
	}
	if _, ok := settingsAt(t, filepath.Join(home, ".gemini", "settings.json"), "model.name"); ok {
		t.Error("model.name should be removed by Default")
	}
}

func TestGeminiAuthDialOnlyViaModel(t *testing.T) {
	home := isolateGemini(t)
	saveFakeProvider(t)
	writeGeminiFile(t, filepath.Join(home, ".gemini", "settings.json"), `{}`)

	g := gemini(home)
	if err := g.Field("auth").Set("dial"); err == nil {
		t.Error("Set(dial) before routing should fail")
	}
	if err := g.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Field("auth").Set("dial"); err != nil {
		t.Errorf("Set(dial) while routed should succeed, got %v", err)
	}
	if got := g.Field("auth").Get(); got != "dial" {
		t.Errorf("auth = %q, want dial", got)
	}
	if got := g.Field("model").Get(); got != "fake/m1" {
		t.Errorf("Set(dial) changed the model to %q", got)
	}
}

func TestGeminiFieldsByKeyAndLabel(t *testing.T) {
	home := isolateGemini(t)
	g := gemini(home)
	for _, key := range []string{"auth", "model", "provider"} {
		if g.Field(key) == nil {
			t.Errorf("no field %q", key)
		}
	}
	if g.Field("Auth") == nil {
		t.Error("the label should find the auth field too")
	}
	if g.Field("provider").Label != "provider" {
		t.Errorf("provider label = %q", g.Field("provider").Label)
	}
	if g.Field("nope") != nil {
		t.Error("unknown field should be nil")
	}
}

func TestGeminiProviderFieldRejectsUnknown(t *testing.T) {
	home := isolateGemini(t)
	saveFakeProvider(t)
	writeGeminiFile(t, filepath.Join(home, ".gemini", "settings.json"), `{}`)
	g := gemini(home)
	if err := g.Field("provider").Set("nope"); err == nil {
		t.Error("an unknown provider should fail")
	}
	if got := g.Field("model").Get(); got != "" {
		t.Errorf("a failed provider Set changed the model to %q", got)
	}
	if err := g.Field("model").Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	// already served by this provider: nothing moves
	if err := g.Field("provider").Set("fake"); err != nil {
		t.Fatal(err)
	}
	if got := g.Field("model").Get(); got != "fake/m2" {
		t.Errorf("model = %q, want fake/m2 kept", got)
	}
}

func TestGeminiNativeModelWithoutRouting(t *testing.T) {
	home := isolateGemini(t)
	settings := filepath.Join(home, ".gemini", "settings.json")
	writeGeminiFile(t, settings, `{"model":{"name":"gemini-2.5-pro"}}`)
	g := gemini(home)
	if err := g.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := settingsAt(t, settings, "model.name"); v != "gemini-2.5-flash" {
		t.Errorf("model.name = %q", v)
	}
}

// fakeJWT is an unsigned JWT with the given payload; dial only reads claims.
func fakeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	b, _ := json.Marshal(claims)
	return h + "." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestAccountNoteAndCodexOwnModelsHidden(t *testing.T) {
	home := isolateGemini(t)
	id := fakeJWT(map[string]any{"email": "dev@example.com"})
	acc := fakeJWT(map[string]any{"exp": 4102444800})
	body, _ := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]any{"id_token": id, "access_token": acc, "refresh_token": "r", "account_id": "acct"},
	})
	writeGeminiFile(t, filepath.Join(home, ".codex", "auth.json"), string(body))

	// the picker of another agent credits the account, not the vendor; Pi
	// spells catalog values as dial/<provider>/<model>
	piAgent := pi(home)
	var found bool
	for _, o := range piAgent.Field("model").Options(nil) {
		if strings.HasPrefix(o.Value, "dial/codex/") {
			found = true
			if o.Note != "dev@example.com · via dial" {
				t.Errorf("codex note = %q, want the account user", o.Note)
			}
		}
	}
	if !found {
		t.Error("other agents should offer dial/codex/… models")
	}

	// Codex's own picker leaves its sign-in models out
	for _, o := range codex(home).Field("model").Options(nil) {
		if strings.HasPrefix(o.Value, "codex/") {
			t.Errorf("codex's own picker lists %q", o.Value)
		}
	}
}

// A bare catalog id, one that is not Gemini's own name, routes when exactly one
// catalog entry has it: the CLI ends up on provider/model, as if it had been named.
func TestGeminiBareCatalogIDRoutesWhenUnique(t *testing.T) {
	home := isolateGemini(t)
	saveFakeProvider(t)
	settings := filepath.Join(home, ".gemini", "settings.json")
	writeGeminiFile(t, settings, `{}`)

	g := gemini(home)
	if err := g.Field("model").Set("m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := settingsAt(t, settings, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q, want fake/m1", v)
	}
	if v, _ := settingsAt(t, settings, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q, want gemini-api-key", v)
	}
}

// A bare id that two providers serve is refused and names both; an id nobody
// serves is refused too. Neither is written as a native model.
func TestGeminiBareCatalogIDAmbiguousOrUnknownIsRefused(t *testing.T) {
	home := isolateGemini(t)
	saveFakeProvider(t)
	if err := provider.Save(provider.Provider{ID: "other", Name: "Other", Key: "k",
		Chat: "http://127.0.0.1:9/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".gemini", "settings.json")
	writeGeminiFile(t, settings, `{"model":{"name":"gemini-2.5-pro"}}`)

	g := gemini(home)
	err := g.Field("model").Set("m1")
	if err == nil || !strings.Contains(err.Error(), "several providers") {
		t.Errorf("ambiguous m1: err = %v, want a several-providers error", err)
	}
	if err := g.Field("model").Set("no-such-model"); err == nil {
		t.Error("unknown bare id was accepted")
	}
	if v, _ := settingsAt(t, settings, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("refused picks changed model.name to %q", v)
	}
	if _, ok := settingsAt(t, settings, "security.auth.selectedType"); ok {
		t.Error("refused pick routed the CLI anyway")
	}
}
