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

// zzGeminiSandbox points HOME and the dial config at a fresh temp dir, fixes
// the gateway address, and saves one fake provider serving model m1.
func zzGeminiSandbox(t *testing.T) (home string, a *Agent) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("DIAL_ADDR", "127.0.0.1:3999")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	return home, gemini(home)
}

func zzGeminiFiles(home string) (settings, env string) {
	return filepath.Join(home, ".gemini", "settings.json"), filepath.Join(home, ".gemini", ".env")
}

func zzWriteGeminiFiles(t *testing.T, home, settings, env string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if env != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func zzGeminiState(t *testing.T, settings, env string) (selected, key, base, model string) {
	t.Helper()
	selected, _ = edit.GetJSON(settings, "security.auth.selectedType")
	model, _ = edit.GetJSON(settings, "model.name")
	key, _ = edit.GetEnvFile(env, "GEMINI_API_KEY")
	base, _ = edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL")
	return
}

func zzMustSet(t *testing.T, f *Field, v string) {
	t.Helper()
	if err := f.Set(v); err != nil {
		t.Fatalf("set %q: %v", v, err)
	}
}

func TestZzGeminiHasProviderAndLabelLookup(t *testing.T) {
	_, a := zzGeminiSandbox(t)
	if a.Field("provider") == nil {
		t.Fatal("gemini has no provider field")
	}
	if a.Field("model") == nil {
		t.Fatal("gemini has no model field")
	}
	f := a.Field("auth")
	if f == nil || f.Key != "provider" {
		t.Fatalf("auth label should find the provider field, got %+v", f)
	}
	if a.Field("nope") != nil {
		t.Fatal("unknown field should not resolve")
	}
}

func TestZzGeminiRouteAndRestoreRoundTrip(t *testing.T) {
	home, a := zzGeminiSandbox(t)
	settings, env := zzGeminiFiles(home)
	zzWriteGeminiFiles(t, home,
		`{"other":"keep","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro","x":1}}`,
		"FOO=bar\nGEMINI_API_KEY=oldkey\n")

	model, auth := a.Field("model"), a.Field("provider")
	zzMustSet(t, model, "fake/m1")

	sel, key, base, m := zzGeminiState(t, settings, env)
	if sel != "gemini-api-key" || key != gateway.Token || base != gateway.URL() || m != "fake/m1" {
		t.Fatalf("routed state wrong: sel=%q key=%q base=%q model=%q", sel, key, base, m)
	}
	if v, _ := edit.GetEnvFile(env, "FOO"); v != "bar" {
		t.Fatalf("unrelated .env line lost: FOO=%q", v)
	}
	if v, _ := edit.GetJSON(settings, "other"); v != "keep" {
		t.Fatalf("unrelated settings field lost: other=%q", v)
	}
	if got := auth.Get(); got != "dial" {
		t.Fatalf("auth while routed = %q, want dial", got)
	}
	if err := auth.Set("dial"); err != nil {
		t.Fatalf("Set(dial) while routed: %v", err)
	}
	if sel2, _, _, _ := zzGeminiState(t, settings, env); sel2 != "gemini-api-key" {
		t.Fatalf("Set(dial) changed state: %q", sel2)
	}
	found := false
	for _, o := range model.Options(nil) {
		if o.Value == "fake/m1" {
			found = true
		}
	}
	if !found {
		t.Fatal("catalog model missing from model options")
	}

	// back to a native model: everything the CLI had comes back
	zzMustSet(t, model, "gemini-2.5-flash")
	sel, key, base, m = zzGeminiState(t, settings, env)
	if sel != "oauth-personal" || key != "oldkey" || base != "" || m != "gemini-2.5-flash" {
		t.Fatalf("restored state wrong: sel=%q key=%q base=%q model=%q", sel, key, base, m)
	}
	if v, _ := edit.GetEnvFile(env, "FOO"); v != "bar" {
		t.Fatalf("FOO lost after restore: %q", v)
	}
	if v, _ := edit.GetJSON(settings, "other"); v != "keep" {
		t.Fatalf("other lost after restore: %q", v)
	}
	if got := auth.Get(); got != "google" {
		t.Fatalf("auth after restore = %q, want google", got)
	}
	if err := auth.Set("dial"); err == nil {
		t.Fatal("Set(dial) unrouted should fail")
	}
}

func TestZzGeminiDefaultRemovesWhatWasAbsent(t *testing.T) {
	home, a := zzGeminiSandbox(t)
	settings, env := zzGeminiFiles(home)
	zzWriteGeminiFiles(t, home, `{"other":"keep"}`, "FOO=bar\n")

	model := a.Field("model")
	zzMustSet(t, model, "fake/m1")
	zzMustSet(t, model, "")

	sel, key, base, m := zzGeminiState(t, settings, env)
	if sel != "" || key != "" || base != "" || m != "" {
		t.Fatalf("Default should remove routing keys: sel=%q key=%q base=%q model=%q", sel, key, base, m)
	}
	raw, _ := os.ReadFile(env)
	if strings.Contains(string(raw), "GEMINI_API_KEY") || strings.Contains(string(raw), "GOOGLE_GEMINI_BASE_URL") {
		t.Fatalf(".env still has routing keys:\n%s", raw)
	}
	if v, _ := edit.GetEnvFile(env, "FOO"); v != "bar" {
		t.Fatalf("FOO lost: %q", v)
	}
	if v, _ := edit.GetJSON(settings, "other"); v != "keep" {
		t.Fatalf("other lost: %q", v)
	}
}

func TestZzGeminiDefaultRestoresKey(t *testing.T) {
	home, a := zzGeminiSandbox(t)
	settings, env := zzGeminiFiles(home)
	zzWriteGeminiFiles(t, home, `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`, "GEMINI_API_KEY=mine\n")

	model := a.Field("model")
	zzMustSet(t, model, "fake/m1")
	zzMustSet(t, model, "")

	sel, key, base, m := zzGeminiState(t, settings, env)
	if sel != "gemini-api-key" || key != "mine" || base != "" || m != "" {
		t.Fatalf("Default: sel=%q key=%q base=%q model=%q", sel, key, base, m)
	}
}

func TestZzGeminiSwitchBetweenCatalogModelsKeepsStash(t *testing.T) {
	home, a := zzGeminiSandbox(t)
	settings, env := zzGeminiFiles(home)
	zzWriteGeminiFiles(t, home, `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, "GEMINI_API_KEY=orig\n")

	model := a.Field("model")
	zzMustSet(t, model, "fake/m1")
	zzMustSet(t, model, "fake/m1")
	zzMustSet(t, model, "gemini-2.5-pro")

	sel, key, _, _ := zzGeminiState(t, settings, env)
	if sel != "oauth-personal" || key != "orig" {
		t.Fatalf("stash overwritten by gateway values: sel=%q key=%q", sel, key)
	}
}

func TestZzGeminiAuthSetOtherLeavesDial(t *testing.T) {
	home, a := zzGeminiSandbox(t)
	settings, env := zzGeminiFiles(home)
	zzWriteGeminiFiles(t, home, `{"security":{"auth":{"selectedType":"vertex-ai"}}}`, "")

	zzMustSet(t, a.Field("model"), "fake/m1")
	zzMustSet(t, a.Field("provider"), "google")

	sel, key, base, m := zzGeminiState(t, settings, env)
	if sel != "oauth-personal" || key != "" || base != "" || m != "" {
		t.Fatalf("leaving dial via auth: sel=%q key=%q base=%q model=%q", sel, key, base, m)
	}
}
