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

// dialxGeminiEnv gives each test its own HOME, dial config and one catalog
// provider ("fake", models m1 and m2).
func dialxGeminiEnv(t *testing.T) (a *Agent, envPath, settings string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1", "m2"}, Chat: "http://upstream.invalid/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	a = gemini(home)
	return a, filepath.Join(home, ".gemini", ".env"), filepath.Join(home, ".gemini", "settings.json")
}

func dialxWrite(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func dialxEnvVal(path, key string) (string, bool)  { return edit.GetEnvFile(path, key) }
func dialxJSONVal(path, key string) (string, bool) { return edit.GetJSON(path, key) }

func dialxMustSet(t *testing.T, a *Agent, field, v string) {
	t.Helper()
	f := a.Field(field)
	if f == nil {
		t.Fatalf("no field %q", field)
	}
	if err := f.Set(v); err != nil {
		t.Fatalf("set %s=%q: %v", field, v, err)
	}
}

func dialxCheckRouted(t *testing.T, a *Agent, envPath, settings, model string) {
	t.Helper()
	if v, _ := dialxEnvVal(envPath, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url %q, want %q", v, gateway.URL())
	}
	if v, _ := dialxEnvVal(envPath, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("api key %q", v)
	}
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("auth type %q", v)
	}
	if v, _ := dialxJSONVal(settings, "model.name"); v != model {
		t.Errorf("model %q, want %q", v, model)
	}
	if v := a.Field("auth").Get(); v != "dial" {
		t.Errorf("auth shows %q, want dial", v)
	}
	if v := a.Field("model").Get(); v != model {
		t.Errorf("model shows %q", v)
	}
}

func TestDialxGeminiRouteCatalogModel(t *testing.T) {
	a, envPath, settings := dialxGeminiEnv(t)
	dialxMustSet(t, a, "model", "fake/m1")
	dialxCheckRouted(t, a, envPath, settings, "fake/m1")

	var dial *Option
	for _, o := range a.Field("auth").Options(a.Values()) {
		if o.Value == "dial" {
			dial = &o
		}
	}
	if dial == nil || !strings.Contains(dial.Note, "gateway") {
		t.Errorf("auth options lack dial: %+v", dial)
	}

	// catalog models come grouped by provider, apart from Gemini's own
	var found bool
	for _, o := range a.Field("model").Options(a.Values()) {
		if o.Value == "fake/m1" {
			found = o.Group == "Fake"
		}
		if strings.HasPrefix(o.Value, "gemini-") && o.Group != "Gemini CLI" {
			t.Errorf("native option %q in group %q", o.Value, o.Group)
		}
	}
	if !found {
		t.Errorf("fake/m1 not offered in its own group")
	}
}

const dialxOrigEnv = "# mine\nOTHER=1\nGOOGLE_GEMINI_BASE_URL=https://proxy.example.com\nGEMINI_API_KEY=my-key\n"
const dialxOrigSettings = `{
  "ui": {"theme": "Dracula"},
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-flash"}
}
`

func dialxCheckOriginal(t *testing.T, envPath, settings string) {
	t.Helper()
	if v, _ := dialxEnvVal(envPath, "GOOGLE_GEMINI_BASE_URL"); v != "https://proxy.example.com" {
		t.Errorf("base url %q", v)
	}
	if v, _ := dialxEnvVal(envPath, "GEMINI_API_KEY"); v != "my-key" {
		t.Errorf("api key %q", v)
	}
	if v, _ := dialxEnvVal(envPath, "OTHER"); v != "1" {
		t.Errorf("other env lost: %q", v)
	}
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("auth type %q", v)
	}
	if v, _ := dialxJSONVal(settings, "ui.theme"); v != "Dracula" {
		t.Errorf("theme lost: %q", v)
	}
}

func TestDialxGeminiRestoreOnNativeModel(t *testing.T) {
	a, envPath, settings := dialxGeminiEnv(t)
	dialxWrite(t, envPath, dialxOrigEnv)
	dialxWrite(t, settings, dialxOrigSettings)

	dialxMustSet(t, a, "model", "fake/m1")
	dialxCheckRouted(t, a, envPath, settings, "fake/m1")
	// switching between catalog models must not overwrite what was saved
	dialxMustSet(t, a, "model", "fake/m2")
	dialxCheckRouted(t, a, envPath, settings, "fake/m2")

	dialxMustSet(t, a, "model", "gemini-2.5-pro")
	dialxCheckOriginal(t, envPath, settings)
	if v, _ := dialxJSONVal(settings, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("model %q", v)
	}
	if v := a.Field("auth").Get(); v == "dial" {
		t.Errorf("still shows dial")
	}

	// a second round trip starts from the restored values, not stale ones
	dialxMustSet(t, a, "model", "fake/m1")
	dialxMustSet(t, a, "model", "gemini-2.5-flash")
	dialxCheckOriginal(t, envPath, settings)
}

func TestDialxGeminiRestoreOnDefault(t *testing.T) {
	a, envPath, settings := dialxGeminiEnv(t)
	dialxWrite(t, envPath, dialxOrigEnv)
	dialxWrite(t, settings, dialxOrigSettings)

	dialxMustSet(t, a, "model", "fake/m1")
	dialxMustSet(t, a, "model", "fake/m2")
	dialxMustSet(t, a, "model", "")
	dialxCheckOriginal(t, envPath, settings)
	if v, ok := dialxJSONVal(settings, "model.name"); ok {
		t.Errorf("model.name left: %q", v)
	}
}

func TestDialxGeminiRestoreAbsent(t *testing.T) {
	for _, back := range []string{"gemini-2.5-pro", ""} {
		a, envPath, settings := dialxGeminiEnv(t)
		dialxWrite(t, settings, `{"ui": {"theme": "Dracula"}}`+"\n")

		dialxMustSet(t, a, "model", "fake/m1")
		dialxMustSet(t, a, "model", "fake/m2")
		dialxMustSet(t, a, "model", back)
		for _, k := range []string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"} {
			if v, ok := dialxEnvVal(envPath, k); ok {
				t.Errorf("%q: %s left as %q", back, k, v)
			}
		}
		if v, ok := dialxJSONVal(settings, "security.auth.selectedType"); ok {
			t.Errorf("%q: auth type left as %q", back, v)
		}
		if _, ok := dialxJSONVal(settings, "security"); ok {
			b, _ := os.ReadFile(settings)
			t.Errorf("%q: empty security block left: %s", back, b)
		}
		m, ok := dialxJSONVal(settings, "model.name")
		if back == "" && ok {
			t.Errorf("model.name left: %q", m)
		}
		if back != "" && m != back {
			t.Errorf("model %q, want %q", m, back)
		}
		if v, _ := dialxJSONVal(settings, "ui.theme"); v != "Dracula" {
			t.Errorf("theme lost: %q", v)
		}
	}
}

func TestDialxGeminiAuthByLabel(t *testing.T) {
	a, envPath, settings := dialxGeminiEnv(t)
	for _, name := range []string{"auth", "AUTH", "provider", " Auth "} {
		if f := a.Field(name); f == nil || f.Key != "provider" {
			t.Errorf("Field(%q) = %v", name, f)
		}
	}
	if f := a.Field("Model"); f == nil || f.Key != "model" {
		t.Errorf("Field(Model) = %v", f)
	}
	if a.Field("nope") != nil {
		t.Errorf("unknown field resolved")
	}

	// label and key set the same thing
	dialxMustSet(t, a, "auth", "vertex")
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "vertex-ai" {
		t.Errorf("via label: %q", v)
	}
	dialxMustSet(t, a, "provider", "google")
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("via key: %q", v)
	}
	if a.Field("auth").Get() != a.Field("provider").Get() {
		t.Errorf("label and key read differently")
	}

	// dial is turned on by picking a catalog model, not by hand
	err := a.Field("auth").Set("dial")
	if err == nil || !strings.Contains(err.Error(), "catalog model") {
		t.Errorf("auth=dial unrouted: %v", err)
	}
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("failed set changed auth: %q", v)
	}

	// once routed, dial is a no-op; any other auth undoes the routing first
	dialxMustSet(t, a, "model", "fake/m1")
	dialxMustSet(t, a, "auth", "dial")
	dialxCheckRouted(t, a, envPath, settings, "fake/m1")
	dialxMustSet(t, a, "auth", "vertex")
	if v, ok := dialxEnvVal(envPath, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("base url left: %q", v)
	}
	if v, ok := dialxEnvVal(envPath, "GEMINI_API_KEY"); ok {
		t.Errorf("gateway key left: %q", v)
	}
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "vertex-ai" {
		t.Errorf("auth after unroute: %q", v)
	}
	if v := a.Field("auth").Get(); v != "vertex" {
		t.Errorf("auth shows %q", v)
	}
}

func TestDialxGeminiAuthChangeRestoresKey(t *testing.T) {
	a, envPath, settings := dialxGeminiEnv(t)
	dialxWrite(t, envPath, "GEMINI_API_KEY=my-key\n")
	dialxWrite(t, settings, `{"security": {"auth": {"selectedType": "gemini-api-key"}}}`+"\n")
	dialxMustSet(t, a, "model", "fake/m1")
	dialxMustSet(t, a, "auth", "google")
	if v, _ := dialxEnvVal(envPath, "GEMINI_API_KEY"); v != "my-key" {
		t.Errorf("own key not restored: %q", v)
	}
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("auth %q", v)
	}
}

func TestDialxGeminiAuthChangeRestoresModel(t *testing.T) {
	// a saved model comes back when routing ends by changing auth
	for _, auth := range []string{"google", "api-key", "vertex"} {
		a, envPath, settings := dialxGeminiEnv(t)
		dialxWrite(t, envPath, dialxOrigEnv)
		dialxWrite(t, settings, dialxOrigSettings)
		dialxMustSet(t, a, "model", "fake/m1")
		dialxMustSet(t, a, "model", "fake/m2")
		dialxMustSet(t, a, "auth", auth)
		if v, _ := dialxJSONVal(settings, "model.name"); v != "gemini-2.5-flash" {
			t.Errorf("%s: model %q, want gemini-2.5-flash", auth, v)
		}
		if v, _ := dialxEnvVal(envPath, "GEMINI_API_KEY"); v != "my-key" {
			t.Errorf("%s: key %q", auth, v)
		}
		if v := a.Field("auth").Get(); v == "dial" {
			t.Errorf("%s: still routed", auth)
		}
	}
	// no model before routing: none after
	a, envPath, settings := dialxGeminiEnv(t)
	dialxWrite(t, settings, `{"ui": {"theme": "Dracula"}}`+"\n")
	dialxMustSet(t, a, "model", "fake/m1")
	dialxMustSet(t, a, "auth", "google")
	if v, ok := dialxJSONVal(settings, "model.name"); ok {
		t.Errorf("model.name left: %q", v)
	}
	if _, ok := dialxJSONVal(settings, "model"); ok {
		b, _ := os.ReadFile(settings)
		t.Errorf("empty model block left: %s", b)
	}
	if v, ok := dialxEnvVal(envPath, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Errorf("base url left: %q", v)
	}
	if v, _ := dialxJSONVal(settings, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("auth %q", v)
	}
}
