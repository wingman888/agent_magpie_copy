package agent

import (
	"strings"
	"testing"

	"github.com/yetone/dial/internal/gateway"
)

// Acceptance check V5: routing dial gemini through the gateway and restoring
// every earlier value on a native model or Default.

func expectEnvValue(t *testing.T, env, key, want string) {
	t.Helper()
	if got, _ := envGet(t, env, key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func expectJSONValue(t *testing.T, path, key, want string) {
	t.Helper()
	if got, _ := jsonAt(t, path, key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func TestAcceptV5GeminiRouteAndRestore(t *testing.T) {
	home := geminiHome(t)
	settings, env := geminiFiles(t, home)
	writeTest(t, settings, `{"other":"x","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	writeTest(t, env, "FOO=bar\nGEMINI_API_KEY=orig-key\nGOOGLE_GEMINI_BASE_URL=https://orig.example\n")
	addFakeVendor(t)
	a := gemini(home)

	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	expectEnvValue(t, env, "GOOGLE_GEMINI_BASE_URL", gateway.URL())
	expectEnvValue(t, env, "GEMINI_API_KEY", gateway.Token)
	expectEnvValue(t, env, "FOO", "bar")
	expectJSONValue(t, settings, "security.auth.selectedType", "gemini-api-key")
	expectJSONValue(t, settings, "model.name", "fake/m1")
	expectJSONValue(t, settings, "other", "x")
	if got := field(t, a, "auth").Get(); got != "dial" {
		t.Errorf("auth shows %q, want dial", got)
	}
	if err := field(t, a, "auth").Set("dial"); err != nil {
		t.Errorf("setting auth to dial while routed: %v", err)
	}
	if got := field(t, a, "provider").Get(); got != "Fake" {
		t.Errorf("provider shows %q, want Fake", got)
	}

	// a native model restores the earlier key, base URL and auth type
	if err := field(t, a, "model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	expectEnvValue(t, env, "GOOGLE_GEMINI_BASE_URL", "https://orig.example")
	expectEnvValue(t, env, "GEMINI_API_KEY", "orig-key")
	expectEnvValue(t, env, "FOO", "bar")
	expectJSONValue(t, settings, "security.auth.selectedType", "oauth-personal")
	expectJSONValue(t, settings, "model.name", "gemini-2.5-flash")
	if got := field(t, a, "auth").Get(); got == "dial" {
		t.Errorf("auth still dial after a native model")
	}

	// route again, then Default restores the same values and clears the model
	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := field(t, a, "model").Set(""); err != nil {
		t.Fatal(err)
	}
	expectEnvValue(t, env, "GOOGLE_GEMINI_BASE_URL", "https://orig.example")
	expectEnvValue(t, env, "GEMINI_API_KEY", "orig-key")
	expectJSONValue(t, settings, "security.auth.selectedType", "oauth-personal")
	expectJSONValue(t, settings, "model.name", "")
	if got := field(t, a, "auth").Get(); got == "dial" {
		t.Errorf("auth still dial after Default")
	}
}

// V5: when nothing was set before routing, restoring removes the entries.
func TestAcceptV5RestoreRemovesAbsentEntries(t *testing.T) {
	home := geminiHome(t)
	settings, env := geminiFiles(t, home)
	writeTest(t, settings, `{"model":{"name":"gemini-2.5-pro"}}`)
	writeTest(t, env, "FOO=bar\n")
	addFakeVendor(t)
	a := gemini(home)

	if err := field(t, a, "auth").Set("dial"); err == nil {
		t.Errorf("auth dial accepted before routing")
	}
	if err := field(t, a, "model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := field(t, a, "model").Set(""); err != nil {
		t.Fatal(err)
	}
	e := readTest(t, env)
	if strings.Contains(e, "GEMINI_API_KEY") || strings.Contains(e, "GOOGLE_GEMINI_BASE_URL") || !strings.Contains(e, "FOO=bar") {
		t.Errorf(".env after Default: %q", e)
	}
	expectJSONValue(t, settings, "security.auth.selectedType", "")
	expectJSONValue(t, settings, "model.name", "")
}
