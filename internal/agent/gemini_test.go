package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// geminiHome points HOME and the dial config dir at a temp directory and
// adds one catalog provider ("fake", models m1 and m2).
func geminiHome(t *testing.T) (string, *Agent) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	t.Setenv("DIAL_ADDR", "")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:9/v1",
		Key: "k", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	return home, gemini(home)
}

func writeGeminiFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// userConfig is a Gemini CLI setup the user already had: Google sign-in, an
// own API key in .env, a custom base URL, a model, and unrelated settings.
func userConfig(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	writeGeminiFile(t, filepath.Join(dir, ".env"),
		"# mine\nGOOGLE_GEMINI_BASE_URL=https://proxy.example/x\nGEMINI_API_KEY=my-own-key\nOTHER=1\n")
	writeGeminiFile(t, filepath.Join(dir, "settings.json"),
		`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"},"theme":"dark"}`+"\n")
}

func envGet(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	v, ok := edit.GetEnvFile(filepath.Join(home, ".gemini", ".env"), key)
	return v, ok
}

func jsonGetT(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	v, ok := edit.GetJSON(filepath.Join(home, ".gemini", "settings.json"), key)
	return v, ok
}

func setField(t *testing.T, a *Agent, key, value string) {
	t.Helper()
	if err := a.Field(key).Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", key, value, err)
	}
}

func expectValues(t *testing.T, home string, base, key, auth, model string) {
	t.Helper()
	check := func(name string, got string, ok bool, want string) {
		t.Helper()
		if want == "" {
			if ok {
				t.Errorf("%s: want absent, got %q", name, got)
			}
			return
		}
		if !ok || got != want {
			t.Errorf("%s: want %q, got %q (present=%v)", name, want, got, ok)
		}
	}
	v, ok := envGet(t, home, "GOOGLE_GEMINI_BASE_URL")
	check("GOOGLE_GEMINI_BASE_URL", v, ok, base)
	v, ok = envGet(t, home, "GEMINI_API_KEY")
	check("GEMINI_API_KEY", v, ok, key)
	v, ok = jsonGetT(t, home, "security.auth.selectedType")
	check("selectedType", v, ok, auth)
	v, ok = jsonGetT(t, home, "model.name")
	check("model.name", v, ok, model)
}

func TestGeminiRouteWritesGatewayAndKeepsOtherLines(t *testing.T) {
	home, a := geminiHome(t)
	userConfig(t, home)

	setField(t, a, "model", "fake/m1")

	expectValues(t, home, gateway.URL(), gateway.Token, geminiAPIKey, "fake/m1")
	if v, _ := envGet(t, home, "OTHER"); v != "1" {
		t.Errorf("unrelated .env line lost: OTHER=%q", v)
	}
	if v, _ := jsonGetT(t, home, "theme"); v != "dark" {
		t.Errorf("unrelated settings field lost: theme=%q", v)
	}
	if s := stashLoad(); s["gemini.base_url"] != "https://proxy.example/x" || s["gemini.api_key"] != "my-own-key" ||
		s["gemini.auth"] != geminiOAuth || s["gemini.model"] != "gemini-2.5-pro" {
		t.Errorf("originals not stashed: %v", s)
	}
	if got := a.Field("provider").Get(); got != dialID {
		t.Errorf("auth shows %q while routed, want %q", got, dialID)
	}
}

func TestGeminiSwitchCatalogModelKeepsOriginals(t *testing.T) {
	home, a := geminiHome(t)
	userConfig(t, home)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "fake/m2")

	expectValues(t, home, gateway.URL(), gateway.Token, geminiAPIKey, "fake/m2")
	setField(t, a, "model", "gemini-2.5-flash")
	expectValues(t, home, "https://proxy.example/x", "my-own-key", geminiOAuth, "gemini-2.5-flash")
	if s := stashLoad(); len(s) != 0 {
		t.Errorf("stash not emptied after restore: %v", s)
	}
}

func TestGeminiNativeModelRestoresExactOriginals(t *testing.T) {
	home, a := geminiHome(t)
	userConfig(t, home)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "gemini-2.5-flash")

	expectValues(t, home, "https://proxy.example/x", "my-own-key", geminiOAuth, "gemini-2.5-flash")
	if v, _ := envGet(t, home, "OTHER"); v != "1" {
		t.Errorf("unrelated .env line lost: OTHER=%q", v)
	}
}

func TestGeminiRestoreRemovesWhatWasAbsent(t *testing.T) {
	home, a := geminiHome(t)

	setField(t, a, "model", "fake/m1")
	expectValues(t, home, gateway.URL(), gateway.Token, geminiAPIKey, "fake/m1")

	setField(t, a, "model", "")

	expectValues(t, home, "", "", "", "")
	if got := a.Field("provider").Get(); got != "google" {
		t.Errorf("auth shows %q after restore, want google", got)
	}
}

func TestGeminiDefaultWhileRoutedRestoresAndClears(t *testing.T) {
	home, a := geminiHome(t)
	userConfig(t, home)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "model", "")

	expectValues(t, home, "https://proxy.example/x", "my-own-key", geminiOAuth, "gemini-2.5-pro")
}

func TestGeminiDefaultWithoutRouteOnlyClearsModel(t *testing.T) {
	home, a := geminiHome(t)
	userConfig(t, home)

	setField(t, a, "model", "")

	expectValues(t, home, "https://proxy.example/x", "my-own-key", geminiOAuth, "")
	if s := stashLoad(); len(s) != 0 {
		t.Errorf("unrouted default should not stash anything: %v", s)
	}
}

func TestGeminiDialAuthWithoutRouteErrors(t *testing.T) {
	home, a := geminiHome(t)
	userConfig(t, home)

	for _, key := range []string{"provider", "auth"} {
		if err := a.Field(key).Set(dialID); err == nil {
			t.Errorf("%s=dial without a route: want error", key)
		}
	}
	expectValues(t, home, "https://proxy.example/x", "my-own-key", geminiOAuth, "gemini-2.5-pro")
}

func TestGeminiOtherAuthLeavesRouteFirst(t *testing.T) {
	home, a := geminiHome(t)
	userConfig(t, home)

	setField(t, a, "model", "fake/m1")
	setField(t, a, "auth", "google")

	expectValues(t, home, "https://proxy.example/x", "my-own-key", geminiOAuth, "gemini-2.5-pro")
}

func TestGeminiFieldByLabelMatchesKey(t *testing.T) {
	_, a := geminiHome(t)
	if a.Field("auth") == nil || a.Field("auth") != a.Field("provider") {
		t.Fatal(`Field("auth") should resolve to the provider field`)
	}
	// Fields[0] must stay the auth field: `dial gemini default` and the GUI/CLI
	// provider lookups read it by position, as they did before routing existed.
	if a.Fields[0].Key != "provider" {
		t.Errorf("provider (auth) should stay the first field, got %q", a.Fields[0].Key)
	}
	if a.Fields[1].Key != "model" {
		t.Errorf("model should be the second field, got %q", a.Fields[1].Key)
	}
}

func TestGeminiModelOptionsGrouped(t *testing.T) {
	_, a := geminiHome(t)
	var native, catalog bool
	for _, o := range a.Field("model").Options(nil) {
		if o.Group == "Google" && len(o.Value) > 7 && o.Value[:7] == "gemini-" {
			native = true
		}
		if o.Value == "fake/m1" && o.Group == "Fake" {
			catalog = true
		}
	}
	if !native || !catalog {
		t.Errorf("model options: native=%v catalog=%v", native, catalog)
	}
}
