package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/catalog"
	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// geminiSandbox points HOME and XDG_CONFIG_HOME at a temp dir (so the stash
// and providers file stay out of the real config), adds one ready provider
// with two models, and returns the Gemini CLI agent for that home.
func geminiSandbox(t *testing.T) (string, *Agent) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	providers := filepath.Join(home, ".config", "dial", "providers.json")
	if err := os.MkdirAll(filepath.Dir(providers), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTest(t, providers, `{"providers":[{"id":"fake","name":"Fake","key":"sk-test","chat":"http://127.0.0.1:9/v1","models":["m1","m2"]}]}`)
	return home, gemini(home)
}

// writeGeminiConfig writes ~/.gemini/settings.json and .env; an empty string
// leaves the file out entirely.
func writeGeminiConfig(t *testing.T, home, settings, env string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		writeTest(t, filepath.Join(dir, "settings.json"), settings)
	}
	if env != "" {
		writeTest(t, filepath.Join(dir, ".env"), env)
	}
}

func writeTest(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// geminiState reads the four values routing touches.
type geminiState struct{ base, key, auth, model string }

func readGemini(home string) geminiState {
	dir := filepath.Join(home, ".gemini")
	settings, env := filepath.Join(dir, "settings.json"), filepath.Join(dir, ".env")
	var s geminiState
	s.base, _ = edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL")
	s.key, _ = edit.GetEnvFile(env, "GEMINI_API_KEY")
	s.auth, _ = edit.GetJSON(settings, "security.auth.selectedType")
	s.model, _ = edit.GetJSON(settings, "model.name")
	return s
}

func mustSet(t *testing.T, a *Agent, key, value string) {
	t.Helper()
	if err := a.Field(key).Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", key, value, err)
	}
}

func TestGeminiRouteWritesGatewayConfig(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home, `{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`, "")

	mustSet(t, a, "model", "fake/m1")

	got := readGemini(home)
	want := geminiState{base: gateway.URL(), key: gateway.Token, auth: "gemini-api-key", model: "fake/m1"}
	if got != want {
		t.Fatalf("routed config = %+v, want %+v", got, want)
	}
	if v := a.Field("provider").Get(); v != "dial" {
		t.Fatalf("auth shows %q while routed, want dial", v)
	}
}

func TestGeminiRestoreNativeModelKeepsOriginals(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home,
		`{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-flash"}}`,
		"GOOGLE_GEMINI_BASE_URL=https://my.proxy.example\nGEMINI_API_KEY=my-own-key\n")

	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "gemini-2.5-pro")

	got := readGemini(home)
	if got.base != "https://my.proxy.example" || got.key != "my-own-key" || got.auth != "gemini-api-key" {
		t.Fatalf("originals not restored: %+v", got)
	}
	if got.model != "gemini-2.5-pro" {
		t.Fatalf("model = %q, want the native model just chosen", got.model)
	}
	if v := a.Field("provider").Get(); v == "dial" {
		t.Fatalf("auth still shows dial after leaving the route")
	}
}

func TestGeminiRestoreDeletesValuesThatWereAbsent(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home, `{"theme":"dark"}`, "OTHER=1\n")

	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "")

	got := readGemini(home)
	if got != (geminiState{}) {
		t.Fatalf("absent values should be removed, got %+v", got)
	}
	env, _ := os.ReadFile(filepath.Join(home, ".gemini", ".env"))
	if string(env) != "OTHER=1\n" {
		t.Fatalf(".env = %q, unrelated lines must survive", env)
	}
	settings, _ := os.ReadFile(filepath.Join(home, ".gemini", "settings.json"))
	if v, _ := edit.GetJSON(filepath.Join(home, ".gemini", "settings.json"), "theme"); v != "dark" || len(settings) == 0 {
		t.Fatalf("unrelated settings lost: %s", settings)
	}
}

func TestGeminiRerouteDoesNotOverwriteOriginals(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home,
		`{"security":{"auth":{"selectedType":"vertex-ai"}},"model":{"name":"gemini-3.5-flash"}}`,
		"GEMINI_API_KEY=orig-key\n")

	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "fake/m2")
	if got := readGemini(home); got.model != "fake/m2" || got.base != gateway.URL() {
		t.Fatalf("second catalog model not applied: %+v", got)
	}
	mustSet(t, a, "model", "gemini-2.5-pro")

	got := readGemini(home)
	if got.auth != "vertex-ai" || got.key != "orig-key" || got.base != "" {
		t.Fatalf("original values were overwritten by the second route: %+v", got)
	}
}

func TestGeminiAuthDialWithoutRouteIsAnError(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "")
	before := readGemini(home)

	if err := a.Field("provider").Set("dial"); err == nil {
		t.Fatal("auth dial without a route should be an error")
	}
	if after := readGemini(home); after != before {
		t.Fatalf("failed auth dial changed config: %+v -> %+v", before, after)
	}
}

func TestGeminiAuthDisplayNameMatchesKey(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home, `{}`, "")

	byLabel := a.Field("auth")
	byKey := a.Field("provider")
	if byLabel == nil || byKey == nil || byLabel != byKey {
		t.Fatalf("auth and provider should resolve to the same field")
	}
	if a.Field("model") == nil {
		t.Fatal("model lookup broke")
	}
	if a.Field("nope") != nil {
		t.Fatal("unknown field should not resolve")
	}

	mustSet(t, a, "auth", "google")
	if got := readGemini(home).auth; got != "oauth-personal" {
		t.Fatalf("auth google via display name wrote %q", got)
	}
}

func TestGeminiNativeAuthLeavesRouteFirst(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home,
		`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-flash"}}`,
		"GEMINI_API_KEY=mine\n")

	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "auth", "google")

	got := readGemini(home)
	if got.base != "" || got.key != "mine" || got.auth != "oauth-personal" || got.model != "gemini-2.5-flash" {
		t.Fatalf("switching auth away from dial should restore originals, got %+v", got)
	}
}

func TestGeminiDefaultModelUnroutes(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "GEMINI_API_KEY=mine\n")

	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "")

	got := readGemini(home)
	if got.base != "" || got.key != "mine" || got.auth != "oauth-personal" || got.model != "" {
		t.Fatalf("default should unroute and clear dial's settings, got %+v", got)
	}
}

func optionValues(opts []Option) []string {
	var out []string
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}

func TestGeminiAuthOptionsUnroutedMatchBaseline(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "")

	got := optionValues(a.Field("provider").Options(nil))
	want := []string{"google", "api-key", "vertex"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unrouted auth options = %v, want %v", got, want)
	}

	mustSet(t, a, "model", "fake/m1")
	got = optionValues(a.Field("provider").Options(nil))
	if got[len(got)-1] != "dial" {
		t.Fatalf("routed auth options = %v, want dial listed last", got)
	}
}

func TestGeminiNativeModelOptionsKeepOrder(t *testing.T) {
	_, a := geminiSandbox(t)
	var native []string
	for _, o := range a.Field("model").Options(nil) {
		if o.Group == "Gemini CLI" {
			native = append(native, o.Value)
		}
	}
	var want []string
	for _, m := range catalog.Provider("google") {
		if strings.HasPrefix(m.ID, "gemini-") {
			want = append(want, m.ID)
		}
	}
	if strings.Join(native, ",") != strings.Join(want, ",") {
		t.Fatalf("native model options = %v, want %v", native, want)
	}
}

func TestGeminiUnknownAuthDoesNotUnroute(t *testing.T) {
	home, a := geminiSandbox(t)
	writeGeminiConfig(t, home, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "GEMINI_API_KEY=mine\n")

	mustSet(t, a, "model", "fake/m1")
	before := readGemini(home)
	if err := a.Field("provider").Set("bogus"); err == nil {
		t.Fatal("unknown auth should be an error")
	}
	if after := readGemini(home); after != before {
		t.Fatalf("unknown auth changed a routed config: %+v -> %+v", before, after)
	}
}
