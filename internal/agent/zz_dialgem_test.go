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

func dgSetup(t *testing.T, env, settings string) (*Agent, string, string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	envP, setP := filepath.Join(dir, ".env"), filepath.Join(dir, "settings.json")
	if env != "" {
		os.WriteFile(envP, []byte(env), 0o644)
	}
	if settings != "" {
		os.WriteFile(setP, []byte(settings), 0o644)
	}
	return gemini(home), envP, setP
}

func dgEnv(p, k string) (string, bool) { return edit.GetEnvFile(p, k) }
func dgJSON(p, k string) string        { v, _ := edit.GetJSON(p, k); return v }

func TestDialGemRouteAndRestore(t *testing.T) {
	a, envP, setP := dgSetup(t,
		"# mine\nOTHER=1\nGEMINI_API_KEY=orig-key\nGOOGLE_GEMINI_BASE_URL=https://proxy.example/x\n",
		`{"theme":"dark","security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	model := a.Field("model")
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := dgEnv(envP, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Fatalf("base %q", v)
	}
	if v, _ := dgEnv(envP, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatalf("key %q", v)
	}
	if dgJSON(setP, "security.auth.selectedType") != "gemini-api-key" || dgJSON(setP, "model.name") != "fake/m1" {
		t.Fatal("settings not routed")
	}
	if got := a.Field("auth").Get(); got != "dial" {
		t.Fatalf("auth shows %q", got)
	}
	// switching catalog models keeps the original stash
	if err := model.Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := dgEnv(envP, "GOOGLE_GEMINI_BASE_URL"); v != "https://proxy.example/x" {
		t.Fatalf("base %q", v)
	}
	if v, _ := dgEnv(envP, "GEMINI_API_KEY"); v != "orig-key" {
		t.Fatalf("key %q", v)
	}
	if dgJSON(setP, "security.auth.selectedType") != "oauth-personal" || dgJSON(setP, "model.name") != "gemini-2.5-flash" {
		t.Fatal("settings not restored")
	}
	b, _ := os.ReadFile(envP)
	if !strings.Contains(string(b), "# mine") || !strings.Contains(string(b), "OTHER=1") {
		t.Fatalf("env lost lines: %s", b)
	}
	b, _ = os.ReadFile(setP)
	if !strings.Contains(string(b), `"theme"`) {
		t.Fatalf("settings lost fields: %s", b)
	}
}

func TestDialGemRouteFromEmpty(t *testing.T) {
	a, envP, setP := dgSetup(t, "", "")
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if _, ok := dgEnv(envP, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("base should be gone")
	}
	if _, ok := dgEnv(envP, "GEMINI_API_KEY"); ok {
		t.Fatal("key should be gone")
	}
	if dgJSON(setP, "security.auth.selectedType") != "" || dgJSON(setP, "model.name") != "gemini-2.5-pro" {
		t.Fatal("settings wrong")
	}
}

func TestDialGemDefaultUnroutes(t *testing.T) {
	a, envP, setP := dgSetup(t, "GEMINI_API_KEY=orig\n", `{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if v, _ := dgEnv(envP, "GEMINI_API_KEY"); v != "orig" {
		t.Fatalf("key %q", v)
	}
	if _, ok := dgEnv(envP, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("base should be gone")
	}
	if dgJSON(setP, "security.auth.selectedType") != "gemini-api-key" || dgJSON(setP, "model.name") != "gemini-2.5-pro" {
		t.Fatal("settings wrong")
	}
	if a.Field("auth").Get() == "dial" {
		t.Fatal("still routed")
	}
}

func TestDialGemManualDial(t *testing.T) {
	a, _, _ := dgSetup(t, "", "")
	err := a.Field("auth").Set("dial")
	if err == nil || !strings.Contains(err.Error(), "catalog model") {
		t.Fatalf("err %v", err)
	}
	a.Field("model").Set("fake/m1")
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Fatal(err)
	}
	// choosing another auth unroutes first
	if err := a.Field("auth").Set("google"); err != nil {
		t.Fatal(err)
	}
	if a.Field("auth").Get() != "google" {
		t.Fatalf("auth %q", a.Field("auth").Get())
	}
}

func TestDialGemFieldByLabel(t *testing.T) {
	a, _, _ := dgSetup(t, "", "")
	if f := a.Field("AUTH"); f == nil || f.Key != "provider" {
		t.Fatal("label lookup failed")
	}
	if f := a.Field("provider"); f == nil || f.Key != "provider" {
		t.Fatal("key lookup failed")
	}
	if a.Field("nope") != nil {
		t.Fatal("unexpected field")
	}
}
