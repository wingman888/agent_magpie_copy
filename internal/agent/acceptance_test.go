package agent

import (
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// accFake registers a provider whose catalog has fake/m1.
func accFake(t *testing.T) {
	t.Helper()
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "https://example.invalid/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
}

func accEnv(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetEnvFile(home+"/.gemini/.env", key)
}

func accJSON(t *testing.T, home, key string) (string, bool) {
	t.Helper()
	return edit.GetJSON(home+"/.gemini/settings.json", key)
}

// 5. Picking a catalog model routes the CLI through the gateway; picking
// a native model puts everything back.
func TestAccGeminiRouteAndRestoreNative(t *testing.T) {
	home := geminiHome(t, seedEnv, seedSettings)
	accFake(t)
	a := gemini(home)

	mustSet(t, a, "model", "fake/m1")
	if v, _ := accEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url = %q, want %q", v, gateway.URL())
	}
	if v, _ := accEnv(t, home, "GEMINI_API_KEY"); v != gateway.Token {
		t.Errorf("key = %q, want %q", v, gateway.Token)
	}
	if v, _ := accJSON(t, home, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Errorf("selectedType = %q", v)
	}
	if v, _ := accJSON(t, home, "model.name"); v != "fake/m1" {
		t.Errorf("model.name = %q", v)
	}
	if v, _ := accEnv(t, home, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
	if v, _ := accJSON(t, home, "other"); v != "keep" {
		t.Errorf("unrelated settings lost: other=%q", v)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Errorf("auth = %q, want dial", got)
	}

	mustSet(t, a, "model", "gemini-2.5-pro")
	if v, _ := accJSON(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType after native = %q, want oauth-personal", v)
	}
	if v, _ := accEnv(t, home, "GEMINI_API_KEY"); v != "mykey" {
		t.Errorf("GEMINI_API_KEY after native = %q, want mykey", v)
	}
	if _, ok := accEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Error("GOOGLE_GEMINI_BASE_URL should be removed: it was absent before routing")
	}
	if v, _ := accJSON(t, home, "model.name"); v != "gemini-2.5-pro" {
		t.Errorf("model.name after native = %q", v)
	}
}

// 5. Default (empty) restores the auth type and key, and drops model.name.
func TestAccGeminiDefaultRestoresAndDropsModel(t *testing.T) {
	home := geminiHome(t, seedEnv, seedSettings)
	accFake(t)
	a := gemini(home)
	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "")
	if v, _ := accJSON(t, home, "security.auth.selectedType"); v != "oauth-personal" {
		t.Errorf("selectedType = %q", v)
	}
	if v, _ := accEnv(t, home, "GEMINI_API_KEY"); v != "mykey" {
		t.Errorf("key = %q", v)
	}
	if _, ok := accJSON(t, home, "model.name"); ok {
		t.Error("model.name should be deleted by Default")
	}
	if _, ok := accEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Error("base url should be gone")
	}
}

// 5. Without a key before routing, Default leaves no GEMINI_API_KEY behind.
func TestAccGeminiDefaultRemovesKeyWhenNoneBefore(t *testing.T) {
	home := geminiHome(t, "FOO=bar\n", seedSettings)
	accFake(t)
	a := gemini(home)
	mustSet(t, a, "model", "fake/m1")
	mustSet(t, a, "model", "")
	if _, ok := accEnv(t, home, "GEMINI_API_KEY"); ok {
		t.Error("GEMINI_API_KEY should not remain when none existed before")
	}
	if v, _ := accEnv(t, home, "FOO"); v != "bar" {
		t.Errorf("unrelated .env line lost: FOO=%q", v)
	}
}

// 5. auth: "dial" can be set only by routing, and reads back as dial.
func TestAccGeminiAuthDialSetRules(t *testing.T) {
	home := geminiHome(t, seedEnv, seedSettings)
	accFake(t)
	a := gemini(home)

	if err := a.Field("auth").Set("dial"); err == nil {
		t.Error("Set(dial) must fail when not routed")
	}
	mustSet(t, a, "model", "fake/m1")
	before := a.Field("auth").Get()
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Errorf("Set(dial) when routed: %v", err)
	}
	if a.Field("auth").Get() != before || before != "dial" {
		t.Errorf("Set(dial) changed the state: %q -> %q", before, a.Field("auth").Get())
	}
	if v, _ := accEnv(t, home, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Errorf("base url changed by Set(dial): %q", v)
	}
}
