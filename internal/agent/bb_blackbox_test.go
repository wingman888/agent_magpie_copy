package agent

// Black-box checks of the Gemini CLI routing criteria, against the agent's
// Fields and the files it leaves in ~/.gemini.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// bb_gemini sets up a home with one catalog provider (fake/m1) and the given
// Gemini files, and returns the Gemini agent and the home.
func bb_gemini(t *testing.T, settings, env string) (*Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "claude"))
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:9/v1",
		Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range All() {
		if a.ID == "gemini" {
			return a, home
		}
	}
	t.Fatal("no gemini agent")
	return nil, ""
}

// bb_geminiState reads what the Gemini CLI would see.
func bb_geminiState(t *testing.T, home string) (sel, model, other string, env map[string]string) {
	t.Helper()
	s := filepath.Join(home, ".gemini", "settings.json")
	e := filepath.Join(home, ".gemini", ".env")
	sel, _ = edit.GetJSON(s, "security.auth.selectedType")
	model, _ = edit.GetJSON(s, "model.name")
	other, _ = edit.GetJSON(s, "other")
	env = map[string]string{}
	for _, k := range []string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY", "FOO"} {
		if v, ok := edit.GetEnvFile(e, k); ok {
			env[k] = v
		}
	}
	return
}

// Acceptance 5, with prior values: routing keeps unrelated lines, a native
// pick restores everything routing replaced.
func TestBB_GeminiRouteThenNativeRestoresPriorValues(t *testing.T) {
	a, home := bb_gemini(t,
		`{"other":"keep","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`,
		"FOO=bar\nGOOGLE_GEMINI_BASE_URL=https://generativelanguage.googleapis.com\nGEMINI_API_KEY=AIza-real\n")

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	sel, model, other, env := bb_geminiState(t, home)
	if sel != "gemini-api-key" || model != "fake/m1" || other != "keep" {
		t.Errorf("routed settings: selectedType=%q model=%q other=%q", sel, model, other)
	}
	if env["GOOGLE_GEMINI_BASE_URL"] != gateway.URL() || env["GEMINI_API_KEY"] != gateway.Token || env["FOO"] != "bar" {
		t.Errorf("routed .env = %v, want base %q key %q and FOO kept", env, gateway.URL(), gateway.Token)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Errorf("auth shows %q while routed, want dial", got)
	}

	if err := a.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	sel, model, other, env = bb_geminiState(t, home)
	if sel != "oauth-personal" || model != "gemini-2.5-flash" || other != "keep" {
		t.Errorf("native settings: selectedType=%q model=%q other=%q", sel, model, other)
	}
	if env["GOOGLE_GEMINI_BASE_URL"] != "https://generativelanguage.googleapis.com" || env["GEMINI_API_KEY"] != "AIza-real" || env["FOO"] != "bar" {
		t.Errorf("restored .env = %v", env)
	}
}

// Acceptance 5, nothing before: Default removes what routing added, and the
// unrelated entries stay.
func TestBB_GeminiDefaultRemovesWhatWasAbsent(t *testing.T) {
	a, home := bb_gemini(t, `{"other":"keep"}`, "FOO=bar\n")
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	sel, model, other, env := bb_geminiState(t, home)
	if sel != "" || model != "" || other != "keep" {
		t.Errorf("after Default: selectedType=%q model=%q other=%q", sel, model, other)
	}
	if _, ok := env["GEMINI_API_KEY"]; ok {
		t.Errorf("GEMINI_API_KEY left behind: %v", env)
	}
	if _, ok := env["GOOGLE_GEMINI_BASE_URL"]; ok {
		t.Errorf("GOOGLE_GEMINI_BASE_URL left behind: %v", env)
	}
	if env["FOO"] != "bar" {
		t.Errorf("FOO lost: %v", env)
	}
}

// The auth field: "dial" only through a model pick, and it reads "dial" while routed.
func TestBB_GeminiAuthDialOnlyThroughModel(t *testing.T) {
	a, _ := bb_gemini(t, `{"other":"keep"}`, "FOO=bar\n")
	if err := a.Field("auth").Set("dial"); err == nil {
		t.Error("Set(dial) without routing should fail")
	}
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Errorf("Set(dial) while routed: %v", err)
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Errorf("auth = %q", got)
	}
}

// The provider field exists and the auth/provider fields are found by label too.
func TestBB_GeminiFieldsByKeyAndLabel(t *testing.T) {
	a, _ := bb_gemini(t, `{}`, "")
	for _, k := range []string{"model", "auth", "provider"} {
		if f := a.Field(k); f == nil {
			t.Errorf("no %s field", k)
		}
	}
	if f := a.Field("auth"); f == nil || f.Key != "auth" {
		t.Errorf("Field(auth) = %+v", f)
	}
}
