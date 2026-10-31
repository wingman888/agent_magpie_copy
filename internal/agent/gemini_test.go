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

func geminiSetup(t *testing.T) (*Agent, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DIAL_ADDR", "")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return gemini(home), filepath.Join(home, ".gemini")
}

func write(t *testing.T, path, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func state(dir string) map[string]string {
	env := filepath.Join(dir, ".env")
	st := filepath.Join(dir, "settings.json")
	m := map[string]string{}
	m["base"], _ = edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL")
	m["key"], _ = edit.GetEnvFile(env, "GEMINI_API_KEY")
	m["auth"], _ = edit.GetJSON(st, "security.auth.selectedType")
	m["model"], _ = edit.GetJSON(st, "model.name")
	return m
}

func TestGeminiRouteAndRestore(t *testing.T) {
	a, dir := geminiSetup(t)
	write(t, filepath.Join(dir, ".env"), "OTHER=1\nGOOGLE_GEMINI_BASE_URL=https://proxy.example/x\nGEMINI_API_KEY=mine\n")
	write(t, filepath.Join(dir, "settings.json"), `{"theme":"dark","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	orig := state(dir)
	model, auth := a.Field("model"), a.Field("auth")
	if auth == nil || auth.Key != "provider" {
		t.Fatalf("label lookup: %v", auth)
	}

	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	got := state(dir)
	if got["base"] != gateway.URL() || got["key"] != gateway.Token || got["auth"] != "gemini-api-key" || got["model"] != "fake/m1" {
		t.Errorf("routed: %v", got)
	}
	if auth.Get() != "dial" {
		t.Errorf("auth shows %q", auth.Get())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".env")); !strings.Contains(string(b), "OTHER=1") {
		t.Errorf("env lost: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); !strings.Contains(string(b), `"theme"`) {
		t.Errorf("settings lost: %s", b)
	}
	hasDial := false
	for _, o := range auth.Options(nil) {
		hasDial = hasDial || o.Value == "dial"
	}
	if !hasDial {
		t.Error("dial option missing while routed")
	}

	// another catalog model must not overwrite the stash
	if err := model.Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if got := state(dir); got["model"] != "fake/m2" || got["base"] != gateway.URL() {
		t.Errorf("switch: %v", got)
	}

	if err := model.Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	got = state(dir)
	want := orig
	want["model"] = "gemini-2.5-flash"
	for k := range want {
		if got[k] != want[k] {
			t.Errorf("%s: got %q want %q", k, got[k], want[k])
		}
	}
	if auth.Get() != "google" && auth.Get() != "custom" {
		t.Errorf("auth after restore %q", auth.Get())
	}
}

func TestGeminiRestoreAbsent(t *testing.T) {
	a, dir := geminiSetup(t)
	model := a.Field("model")
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	got := state(dir)
	if got["base"] != "" || got["key"] != "" || got["auth"] != "" || got["model"] != "gemini-2.5-pro" {
		t.Errorf("absent items not removed: %v", got)
	}
}

func TestGeminiDefaultClears(t *testing.T) {
	a, dir := geminiSetup(t)
	write(t, filepath.Join(dir, ".env"), "GEMINI_API_KEY=mine\n")
	write(t, filepath.Join(dir, "settings.json"), `{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	model := a.Field("model")
	model.Set("fake/m1")
	model.Set("fake/m2")
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	got := state(dir)
	if got["base"] != "" || got["key"] != "mine" || got["auth"] != "oauth-personal" || got["model"] != "" {
		t.Errorf("default: %v", got)
	}
	if len(stashLoad()) != 0 {
		t.Errorf("stash left: %v", stashLoad())
	}
}

func TestGeminiManualDialAuth(t *testing.T) {
	a, dir := geminiSetup(t)
	auth := a.Field("AUTH")
	if auth == nil {
		t.Fatal("no auth field")
	}
	err := auth.Set("dial")
	if err == nil || !strings.Contains(err.Error(), "catalog model") {
		t.Errorf("want guidance error, got %v", err)
	}
	if got := state(dir); got["base"] != "" || got["auth"] != "" {
		t.Errorf("touched config: %v", got)
	}

	// routed: choosing another auth undoes the routing first
	write(t, filepath.Join(dir, "settings.json"), `{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	a.Field("model").Set("fake/m1")
	if err := auth.Set("dial"); err != nil {
		t.Errorf("dial while routed: %v", err)
	}
	if err := auth.Set("vertex"); err != nil {
		t.Fatal(err)
	}
	got := state(dir)
	if got["base"] != "" || got["key"] != "" || got["auth"] != "vertex-ai" || got["model"] != "gemini-2.5-pro" {
		t.Errorf("after auth switch: %v", got)
	}
}
