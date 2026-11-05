package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

// zvGeminiHome points HOME and the dial config at a temp dir and seeds ~/.gemini.
func zvGeminiHome(t *testing.T, env, settings string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

func zvGeminiEnv(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(filepath.Join(home, ".gemini", ".env"), key)
}

func zvGeminiJSON(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(filepath.Join(home, ".gemini", "settings.json"), key)
}

func zvMustSet(t *testing.T, a *Agent, field, value string) {
	t.Helper()
	if err := a.Field(field).Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", field, value, err)
	}
}

const zvSeedEnv = "FOO=bar\nGEMINI_API_KEY=mykey\n"
const zvSeedSettings = `{
  "other": "keep",
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`

func TestZv_GeminiRouteToCatalogAndBack(t *testing.T) {
	home := zvGeminiHome(t, zvSeedEnv, zvSeedSettings)
	a := gemini(home)

	zvMustSet(t, a, "model", "fake/m1")
	if v, _ := zvGeminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Fatalf("base url = %q, want %q", v, gateway.URL())
	}
	if v, _ := zvGeminiEnv(t, home, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatalf("key = %q, want gateway token", v)
	}
	if v, _ := zvGeminiJSON(t, home, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Fatalf("selectedType = %q", v)
	}
	if v, _ := zvGeminiJSON(t, home, "model.name"); v != "fake/m1" {
		t.Fatalf("model.name = %q", v)
	}
	if v, _ := zvGeminiEnv(t, home, "FOO"); v != "bar" {
		t.Fatalf("unrelated .env line lost: FOO=%q", v)
	}
	if v, _ := zvGeminiJSON(t, home, "other"); v != "keep" {
		t.Fatalf("unrelated settings field lost: other=%q", v)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Fatalf("auth while routed = %q, want dial", got)
	}
	if got := a.Field("provider").Get(); got != "fake" {
		t.Fatalf("provider while routed = %q, want fake", got)
	}

	// a second catalog pick must not overwrite the saved originals
	zvMustSet(t, a, "model", "fake/m2")
	zvMustSet(t, a, "model", "gemini-2.5-flash")

	if v, _ := zvGeminiEnv(t, home, "GEMINI_API_KEY"); v != "mykey" {
		t.Fatalf("key after native pick = %q, want mykey", v)
	}
	if _, ok := zvGeminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatalf("base url should be removed (it was absent before routing)")
	}
	if v, _ := zvGeminiJSON(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatalf("selectedType = %q, want oauth-personal", v)
	}
	if v, _ := zvGeminiJSON(t, home, "model.name"); v != "gemini-2.5-flash" {
		t.Fatalf("model.name = %q, want the native pick", v)
	}
	if v, _ := zvGeminiEnv(t, home, "FOO"); v != "bar" {
		t.Fatalf("FOO lost after restore")
	}
	if v, _ := zvGeminiJSON(t, home, "other"); v != "keep" {
		t.Fatalf("other lost after restore")
	}
}

func TestZv_GeminiDefaultRestoresAndDropsModel(t *testing.T) {
	home := zvGeminiHome(t, zvSeedEnv+"GOOGLE_GEMINI_BASE_URL=https://corp.example\n", zvSeedSettings)
	a := gemini(home)

	zvMustSet(t, a, "model", "fake/m1")
	if v, _ := zvGeminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Fatalf("base url while routed = %q", v)
	}
	zvMustSet(t, a, "model", "")

	if v, _ := zvGeminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != "https://corp.example" {
		t.Fatalf("base url = %q, want the original custom one", v)
	}
	if v, _ := zvGeminiEnv(t, home, "GEMINI_API_KEY"); v != "mykey" {
		t.Fatalf("key = %q, want mykey", v)
	}
	if v, _ := zvGeminiJSON(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatalf("selectedType = %q", v)
	}
	if _, ok := zvGeminiJSON(t, home, "model.name"); ok {
		t.Fatalf("model.name should be removed by Default")
	}
}

func TestZv_GeminiNoKeyBeforeIsRemoved(t *testing.T) {
	home := zvGeminiHome(t, "FOO=bar\n", `{"security":{"auth":{"selectedType":"oauth-personal"}}}`)
	a := gemini(home)

	zvMustSet(t, a, "model", "fake/m1")
	if v, _ := zvGeminiEnv(t, home, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatalf("key while routed = %q", v)
	}
	zvMustSet(t, a, "model", "")

	if _, ok := zvGeminiEnv(t, home, "GEMINI_API_KEY"); ok {
		t.Fatalf("GEMINI_API_KEY should be gone: there was none before routing")
	}
	if _, ok := zvGeminiEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatalf("GOOGLE_GEMINI_BASE_URL should be gone")
	}
	if v, _ := zvGeminiEnv(t, home, "FOO"); v != "bar" {
		t.Fatalf("FOO lost")
	}
	if v, _ := zvGeminiJSON(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatalf("selectedType = %q", v)
	}
}

func TestZv_GeminiNativeModelWithoutRouteKeepsFiles(t *testing.T) {
	home := zvGeminiHome(t, zvSeedEnv, zvSeedSettings)
	a := gemini(home)
	zvMustSet(t, a, "model", "gemini-2.5-flash")
	if v, _ := zvGeminiEnv(t, home, "GEMINI_API_KEY"); v != "mykey" {
		t.Fatalf("key changed by a native pick: %q", v)
	}
	if v, _ := zvGeminiJSON(t, home, "model.name"); v != "gemini-2.5-flash" {
		t.Fatalf("model.name = %q", v)
	}
}

func TestZv_GeminiAuthDialGetAndSet(t *testing.T) {
	home := zvGeminiHome(t, zvSeedEnv, zvSeedSettings)
	a := gemini(home)
	auth := a.Field("auth")

	if got := auth.Get(); got == "dial" {
		t.Fatalf("auth shows dial before routing")
	}
	if err := auth.Set("dial"); err == nil {
		t.Fatalf("Set(dial) must fail when not routed")
	}

	zvMustSet(t, a, "model", "fake/m1")
	if got := auth.Get(); got != "dial" {
		t.Fatalf("auth while routed = %q, want dial", got)
	}
	if err := auth.Set("dial"); err != nil {
		t.Fatalf("Set(dial) while routed: %v", err)
	}
	if v, _ := zvGeminiJSON(t, home, "model.name"); v != "fake/m1" {
		t.Fatalf("Set(dial) changed the model to %q", v)
	}
	if v, _ := zvGeminiEnv(t, home, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatalf("Set(dial) changed the key to %q", v)
	}
}

func TestZv_GeminiFieldsByLabelAndKey(t *testing.T) {
	a := gemini(zvGeminiHome(t, "", ""))
	byKey := a.Field("auth")
	if byKey == nil {
		t.Fatal("no field under key auth")
	}
	for _, q := range []string{"Auth", "AUTH", "auth"} {
		if f := a.Field(q); f != byKey {
			t.Fatalf("Field(%q) did not find the auth field", q)
		}
	}
	if a.Field("provider") == nil {
		t.Fatal("no provider field")
	}
	if a.Field("model") == nil {
		t.Fatal("no model field")
	}
	if a.Field("nope") != nil {
		t.Fatal("unknown field resolved")
	}
	if !strings.Contains(a.Field("provider").Label, "provider") {
		t.Fatal("provider label wrong")
	}
}
