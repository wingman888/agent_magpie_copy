package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// l4GmTEnv points HOME and XDG_CONFIG_HOME at a fresh temp tree, writes a
// provider "fake" with one model, and returns the Gemini CLI agent plus its
// ~/.gemini directory.
func l4GmTEnv(t *testing.T) (*Agent, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	providers := filepath.Join(root, "config", "dial", "providers.json")
	l4GmTWrite(t, providers, `{"providers":[{"id":"fake","name":"Fake","key":"sk-fake","chat":"http://127.0.0.1:1/v1","models":["m1"]}]}`)
	home := filepath.Join(root, "home")
	return gemini(home), filepath.Join(home, ".gemini")
}

func l4GmTWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func l4GmTSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, name := range []string{"settings.json", ".env"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				out[name] = "<absent>"
				continue
			}
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func l4GmTSet(t *testing.T, a *Agent, key, value string) error {
	t.Helper()
	f := a.Field(key)
	if f == nil {
		t.Fatalf("no field %q", key)
	}
	return f.Set(value)
}

func l4GmTGet(a *Agent, key string) string {
	if f := a.Field(key); f != nil {
		return f.Get()
	}
	return "<no field>"
}

func l4GmTEnvVal(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(filepath.Join(dir, ".env"), key)
}

func l4GmTSettingsVal(t *testing.T, dir, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(filepath.Join(dir, "settings.json"), key)
}

func TestL4GmTRouteToCatalogWritesGatewayAndKeepsOthers(t *testing.T) {
	a, dir := l4GmTEnv(t)
	l4GmTWrite(t, filepath.Join(dir, "settings.json"),
		`{"other":"x","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	l4GmTWrite(t, filepath.Join(dir, ".env"), "FOO=bar\n")

	if err := l4GmTSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, ok := l4GmTEnvVal(t, dir, "GOOGLE_GEMINI_BASE_URL"); !ok || v != gateway.URL() {
		t.Fatalf("GOOGLE_GEMINI_BASE_URL = %q %v, want %q", v, ok, gateway.URL())
	}
	if v, ok := l4GmTEnvVal(t, dir, "GEMINI_API_KEY"); !ok || v != gateway.Token {
		t.Fatalf("GEMINI_API_KEY = %q %v, want %q", v, ok, gateway.Token)
	}
	if v, ok := l4GmTEnvVal(t, dir, "FOO"); !ok || v != "bar" {
		t.Fatalf("unrelated FOO lost: %q %v", v, ok)
	}
	if v, _ := l4GmTSettingsVal(t, dir, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Fatalf("selectedType = %q, want gemini-api-key", v)
	}
	if v, _ := l4GmTSettingsVal(t, dir, "model.name"); v != "fake/m1" {
		t.Fatalf("model.name = %q, want fake/m1", v)
	}
	if v, _ := l4GmTSettingsVal(t, dir, "other"); v != "x" {
		t.Fatalf("unrelated settings field lost: %q", v)
	}
	if got := l4GmTGet(a, "provider"); got != "dial" {
		t.Fatalf("auth Get = %q, want dial", got)
	}
	if got := l4GmTGet(a, "model"); got != "fake/m1" {
		t.Fatalf("model Get = %q, want fake/m1", got)
	}
}

func TestL4GmTNativeModelRestoresPriorAuth(t *testing.T) {
	a, dir := l4GmTEnv(t)
	l4GmTWrite(t, filepath.Join(dir, "settings.json"),
		`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	l4GmTWrite(t, filepath.Join(dir, ".env"), "FOO=bar\nGEMINI_API_KEY=AIza-old\n")

	if err := l4GmTSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := l4GmTSet(t, a, "model", "gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := l4GmTSettingsVal(t, dir, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatalf("selectedType = %q, want oauth-personal", v)
	}
	if v, ok := l4GmTEnvVal(t, dir, "GEMINI_API_KEY"); !ok || v != "AIza-old" {
		t.Fatalf("GEMINI_API_KEY = %q %v, want AIza-old", v, ok)
	}
	if _, ok := l4GmTEnvVal(t, dir, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("GOOGLE_GEMINI_BASE_URL should be removed: it was not there before routing")
	}
	if v, _ := l4GmTSettingsVal(t, dir, "model.name"); v != "gemini-2.5-flash" {
		t.Fatalf("model.name = %q, want the native model just chosen", v)
	}
	if v, ok := l4GmTEnvVal(t, dir, "FOO"); !ok || v != "bar" {
		t.Fatalf("unrelated FOO lost: %q %v", v, ok)
	}
	if got := l4GmTGet(a, "provider"); got != "google" {
		t.Fatalf("auth Get = %q, want google", got)
	}
}

func TestL4GmTDefaultRestoresAndDropsKey(t *testing.T) {
	a, dir := l4GmTEnv(t)
	l4GmTWrite(t, filepath.Join(dir, "settings.json"), `{"model":{"name":"gemini-2.5-pro"}}`)
	l4GmTWrite(t, filepath.Join(dir, ".env"), "FOO=bar\n")

	if err := l4GmTSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := l4GmTSet(t, a, "model", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := l4GmTSettingsVal(t, dir, "model.name"); ok {
		t.Fatal("model.name should be removed by Default")
	}
	if _, ok := l4GmTSettingsVal(t, dir, "security.auth.selectedType"); ok {
		t.Fatal("selectedType should be removed: it was not there before routing")
	}
	if _, ok := l4GmTEnvVal(t, dir, "GEMINI_API_KEY"); ok {
		t.Fatal("GEMINI_API_KEY should be removed: there was no key before routing")
	}
	if _, ok := l4GmTEnvVal(t, dir, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("GOOGLE_GEMINI_BASE_URL should be removed")
	}
	if v, ok := l4GmTEnvVal(t, dir, "FOO"); !ok || v != "bar" {
		t.Fatalf("unrelated FOO lost: %q %v", v, ok)
	}
	if got := l4GmTGet(a, "provider"); got == "dial" {
		t.Fatalf("auth still reads dial after Default")
	}
}

func TestL4GmTAuthDialNeedsRouting(t *testing.T) {
	a, dir := l4GmTEnv(t)
	l4GmTWrite(t, filepath.Join(dir, "settings.json"), `{"security":{"auth":{"selectedType":"oauth-personal"}}}`)

	before := l4GmTSnapshot(t, dir)
	if err := l4GmTSet(t, a, "provider", "dial"); err == nil {
		t.Fatal("Set(dial) on an unrouted CLI should fail")
	}
	if got := l4GmTSnapshot(t, dir); got["settings.json"] != before["settings.json"] || got[".env"] != before[".env"] {
		t.Fatal("failed Set(dial) changed the config")
	}

	if err := l4GmTSet(t, a, "model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	routed := l4GmTSnapshot(t, dir)
	if err := l4GmTSet(t, a, "provider", "dial"); err != nil {
		t.Fatalf("Set(dial) on a routed CLI should succeed: %v", err)
	}
	if got := l4GmTSnapshot(t, dir); got["settings.json"] != routed["settings.json"] || got[".env"] != routed[".env"] {
		t.Fatal("Set(dial) on a routed CLI changed the config")
	}
	if got := l4GmTGet(a, "provider"); got != "dial" {
		t.Fatalf("auth Get = %q, want dial", got)
	}
}

func TestL4GmTUnknownCatalogReferenceIsRejected(t *testing.T) {
	a, dir := l4GmTEnv(t)
	l4GmTWrite(t, filepath.Join(dir, "settings.json"), `{"model":{"name":"gemini-2.5-pro"}}`)
	before := l4GmTSnapshot(t, dir)
	if err := l4GmTSet(t, a, "model", "nope/zzz"); err == nil {
		t.Fatal("an unknown catalog reference should be rejected")
	}
	if got := l4GmTSnapshot(t, dir); got["settings.json"] != before["settings.json"] {
		t.Fatal("rejected model changed settings.json")
	}
}

func TestL4GmTFieldsFoundByKeyOrLabel(t *testing.T) {
	a, _ := l4GmTEnv(t)
	if a.Field("provider") == nil || a.Field("provider") != a.Field("auth") {
		t.Fatal(`Field("auth") should resolve to the "provider" field by its label`)
	}
	if a.Field("model") == nil {
		t.Fatal(`Field("model") missing`)
	}
	if a.Field("nope") != nil {
		t.Fatal("unknown field resolved")
	}
}
