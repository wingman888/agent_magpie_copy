package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// zvAccFake registers a provider whose catalog has fake/m1.
func zvAccFake(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "https://example.invalid/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
}

func zvAccEnv(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(home+"/.gemini/.env", key)
}

func zvAccJSON(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(home+"/.gemini/settings.json", key)
}

// 5. Picking a catalog model routes the CLI through the gateway; picking
// a native model puts everything back.
func TestZv_AccGeminiRouteAndRestoreNative(t *testing.T) {
	home := zvAccGeminiHome(t, zvAccSeedEnv, zvAccSeedSettings)
	zvAccFake(t)
	a := gemini(home)

	zvAccMustSet(t, a, "model", "fake/m1")
	if v, _ := zvAccEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url = %q, want %q", v, gateway.URL())
	}
	if v, _ := zvAccEnv(t, home, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("key = %q, want %q", v, gateway.Token)
	}
	if v, _ := zvAccJSON(t, home, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q", v)
	}
	if v, _ := zvAccJSON(t, home, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q", v)
	}
	if v, _ := zvAccEnv(t, home, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if v, _ := zvAccJSON(t, home, "other"); v != "keep" {
		t.Errorf("unrelated settings lost: other=%q", v)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Errorf("auth = %q, want dial", got)
	}

	zvAccMustSet(t, a, "model", "gemini-2.5-pro")
	if v, _ := zvAccJSON(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType after native = %q, want oauth-personal", v)
	}
	if v, _ := zvAccEnv(t, home, "GEMINI_API_KEY"); v != "mykey" {
		t.Errorf("GEMINI_API_KEY after native = %q, want mykey", v)
	}
	if _, ok := zvAccEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Error("GOOGLE_GEMINI_BASE_URL should be removed: it was absent before routing")
	}
	if v, _ := zvAccJSON(t, home, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("model.name after native = %q", v)
	}
}

// 5. Default (empty) restores the auth type and key, and drops model.name.
func TestZv_AccGeminiDefaultRestoresAndDropsModel(t *testing.T) {
	home := zvAccGeminiHome(t, zvAccSeedEnv, zvAccSeedSettings)
	zvAccFake(t)
	a := gemini(home)
	zvAccMustSet(t, a, "model", "fake/m1")
	zvAccMustSet(t, a, "model", "")
	if v, _ := zvAccJSON(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q", v)
	}
	if v, _ := zvAccEnv(t, home, "GEMINI_API_KEY"); v != "mykey" {
		t.Errorf("key = %q", v)
	}
	if _, ok := zvAccJSON(t, home, "model.name"); ok {
		t.Error("model.name should be deleted by Default")
	}
	if _, ok := zvAccEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Error("base url should be gone")
	}
}

// 5. Without a key before routing, Default leaves no GEMINI_API_KEY behind.
func TestZv_AccGeminiDefaultRemovesKeyWhenNoneBefore(t *testing.T) {
	home := zvAccGeminiHome(t, "FOO=bar\n", zvAccSeedSettings)
	zvAccFake(t)
	a := gemini(home)
	zvAccMustSet(t, a, "model", "fake/m1")
	zvAccMustSet(t, a, "model", "")
	if _, ok := zvAccEnv(t, home, "GEMINI_API_KEY"); ok {
		t.Error("GEMINI_API_KEY should not remain when none existed before")
	}
	if v, _ := zvAccEnv(t, home, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
}

// 5. auth: "dial" can be set only by routing, and reads back as dial.
func TestZv_AccGeminiAuthDialSetRules(t *testing.T) {
	home := zvAccGeminiHome(t, zvAccSeedEnv, zvAccSeedSettings)
	zvAccFake(t)
	a := gemini(home)

	if err := a.Field("auth").Set("dial"); err == nil {
		t.Error("Set(dial) must fail when not routed")
	}
	zvAccMustSet(t, a, "model", "fake/m1")
	before := a.Field("auth").Get()
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Errorf("Set(dial) when routed: %v", err)
	}
	if a.Field("auth").Get() != before || before != "dial" {
		t.Errorf("Set(dial) changed the state: %q -> %q", before, a.Field("auth").Get())
	}
	if v, _ := zvAccEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url changed by Set(dial): %q", v)
	}
}

// Local copies of helpers this file needs (kept here so the file stands alone).

func zvAccGeminiHome(t *testing.T, env, settings string) string {
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

func zvAccMustSet(t *testing.T, a *Agent, field, value string) {
	t.Helper()
	if err := a.Field(field).Set(value); err != nil {
		t.Fatalf("set %s=%q: %v", field, value, err)
	}
}

const zvAccSeedEnv = "FOO=bar\nGEMINI_API_KEY=mykey\n"
const zvAccSeedSettings = `{
  "other": "keep",
  "security": {"auth": {"selectedType": "oauth-personal"}},
  "model": {"name": "gemini-2.5-pro"}
}
`
