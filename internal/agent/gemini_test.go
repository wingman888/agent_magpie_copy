package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/edit"
	"github.com/yetone/dial/internal/gateway"
)

func geminiEnv(t *testing.T) (*Agent, string) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "cfg"))
	t.Setenv("CODEX_HOME", "")
	return gemini(h), filepath.Join(h, ".gemini")
}

func put(t *testing.T, p, s string) {
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGeminiRouteAndRestore(t *testing.T) {
	a, dir := geminiEnv(t)
	sp, ep := filepath.Join(dir, "settings.json"), filepath.Join(dir, ".env")
	put(t, sp, `{"other":1,"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`)
	put(t, ep, "FOO=bar\nGEMINI_API_KEY=orig\n")
	model, auth := a.Field("model"), a.Field("auth")
	if auth == nil || a.Field("provider") == nil || auth != a.Field("provider") {
		t.Fatal("field lookup")
	}
	if err := auth.Set("dial"); err == nil {
		t.Fatal("dial should be refused when unrouted")
	}
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetEnvFile(ep, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Fatal(v)
	}
	if v, _ := edit.GetEnvFile(ep, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatal(v)
	}
	if v, _ := edit.GetEnvFile(ep, "FOO"); v != "bar" {
		t.Fatal("FOO lost")
	}
	if v, _ := edit.GetJSON(sp, "other"); v != "1" {
		t.Fatal("other lost")
	}
	if model.Get() != "fake/m1" || auth.Get() != "dial" {
		t.Fatal(model.Get(), auth.Get())
	}
	if err := auth.Set("dial"); err != nil {
		t.Fatal(err)
	}
	// switch between catalog models keeps the original backup
	if err := model.Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetEnvFile(ep, "GEMINI_API_KEY"); v != "orig" {
		t.Fatal(v)
	}
	if _, ok := edit.GetEnvFile(ep, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("base remains")
	}
	if auth.Get() != "google" || model.Get() != "gemini-2.5-flash" {
		t.Fatal(auth.Get(), model.Get())
	}
	if v, _ := edit.GetJSON(sp, "security.auth.selectedType"); v != "oauth-personal" {
		t.Fatal(v)
	}
}

func TestGeminiDefaultRestoresAbsent(t *testing.T) {
	a, dir := geminiEnv(t)
	sp, ep := filepath.Join(dir, "settings.json"), filepath.Join(dir, ".env")
	put(t, sp, `{"other":true}`)
	put(t, ep, "FOO=bar\n")
	model := a.Field("model")
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ep)
	if strings.Contains(string(b), "GEMINI_API_KEY") || strings.Contains(string(b), "GOOGLE_GEMINI_BASE_URL") || !strings.Contains(string(b), "FOO=bar") {
		t.Fatal(string(b))
	}
	if _, ok := edit.GetJSON(sp, "security.auth.selectedType"); ok {
		t.Fatal("selectedType remains")
	}
	if _, ok := edit.GetJSON(sp, "model.name"); ok {
		t.Fatal("model.name remains")
	}
	if v, _ := edit.GetJSON(sp, "other"); v != "true" {
		t.Fatal("other lost")
	}
}
