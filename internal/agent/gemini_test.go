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

func geminiEnv(t *testing.T) (*Agent, string, string) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	return gemini(home), filepath.Join(dir, "settings.json"), filepath.Join(dir, ".env")
}

func jv(p, k string) (string, bool) { return edit.GetJSON(p, k) }
func ev(p, k string) (string, bool) { return edit.GetEnvFile(p, k) }

func TestGeminiRouteAndRestore(t *testing.T) {
	a, sp, ep := geminiEnv(t)
	os.WriteFile(ep, []byte("# mine\nFOO=bar\nGEMINI_API_KEY=mykey\nGOOGLE_GEMINI_BASE_URL=https://x.example\n"), 0o600)
	os.WriteFile(sp, []byte(`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`), 0o600)

	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := ev(ep, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Fatal(v)
	}
	if v, _ := ev(ep, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatal(v)
	}
	if v, _ := jv(sp, "security.auth.selectedType"); v != "gemini-api-key" {
		t.Fatal(v)
	}
	if v, _ := jv(sp, "model.name"); v != "fake/m1" {
		t.Fatal(v)
	}
	if a.Field("auth").Get() != "dial" {
		t.Fatal(a.Field("auth").Get())
	}
	// second switch keeps the original stash
	if err := a.Field("model").Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := ev(ep, "GOOGLE_GEMINI_BASE_URL"); v != "https://x.example" {
		t.Fatal(v)
	}
	if v, _ := ev(ep, "GEMINI_API_KEY"); v != "mykey" {
		t.Fatal(v)
	}
	if v, _ := jv(sp, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatal(v)
	}
	if v, _ := jv(sp, "model.name"); v != "gemini-2.5-flash" {
		t.Fatal(v)
	}
	b, _ := os.ReadFile(ep)
	if !strings.Contains(string(b), "# mine") || !strings.Contains(string(b), "FOO=bar") {
		t.Fatal(string(b))
	}
}

func TestGeminiDefaultRemovesAbsent(t *testing.T) {
	a, sp, ep := geminiEnv(t)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := ev(ep, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("base url left")
	}
	if _, ok := ev(ep, "GEMINI_API_KEY"); ok {
		t.Fatal("key left")
	}
	if _, ok := jv(sp, "security.auth.selectedType"); ok {
		t.Fatal("auth left")
	}
	if _, ok := jv(sp, "model.name"); ok {
		t.Fatal("model left")
	}
}

func TestGeminiAuthDial(t *testing.T) {
	a, sp, ep := geminiEnv(t)
	err := a.Field("auth").Set("dial")
	if err == nil || !strings.Contains(err.Error(), "catalog") {
		t.Fatal(err)
	}
	os.WriteFile(sp, []byte(`{"security":{"auth":{"selectedType":"vertex-ai"}}}`), 0o600)
	a.Field("model").Set("fake/m1")
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("auth").Set("google"); err != nil {
		t.Fatal(err)
	}
	if v, _ := jv(sp, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatal(v)
	}
	if _, ok := ev(ep, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("still routed")
	}
	if _, ok := jv(sp, "model.name"); ok {
		t.Fatal("model left")
	}
}

func TestFieldByLabel(t *testing.T) {
	a, _, _ := geminiEnv(t)
	if f := a.Field("AUTH"); f == nil || f.Key != "provider" {
		t.Fatal(f)
	}
	if f := a.Field("Model"); f == nil || f.Key != "model" {
		t.Fatal(f)
	}
	if a.Field("nope") != nil {
		t.Fatal("unexpected")
	}
}
