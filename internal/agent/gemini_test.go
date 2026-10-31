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

func geminiEnv(t *testing.T) (*Agent, string) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, "cfg"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return gemini(h), filepath.Join(h, ".gemini")
}

func envVal(dir, k string) (string, bool) { return edit.GetEnvFile(filepath.Join(dir, ".env"), k) }
func setting(dir, k string) string {
	v, _ := edit.GetJSON(filepath.Join(dir, "settings.json"), k)
	return v
}

func TestGeminiRouteAndRestore(t *testing.T) {
	a, dir := geminiEnv(t)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("FOO=bar\nGEMINI_API_KEY=orig\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"other":1,"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`), 0o600)
	model, auth := a.Field("model"), a.Field("auth")
	if a.Field("provider") == nil || auth == nil || model == nil {
		t.Fatal("missing fields")
	}
	if err := auth.Set("dial"); err == nil {
		t.Fatal("Set(dial) must fail when not routed")
	}
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if v, _ := envVal(dir, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
		t.Fatalf("base %q", v)
	}
	if v, _ := envVal(dir, "GEMINI_API_KEY"); v != gateway.Token {
		t.Fatalf("key %q", v)
	}
	if v, _ := envVal(dir, "FOO"); v != "bar" {
		t.Fatal("FOO lost")
	}
	if setting(dir, "security.auth.selectedType") != "gemini-api-key" || setting(dir, "model.name") != "fake/m1" {
		t.Fatal("settings not routed")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "settings.json")); !strings.Contains(string(b), `"other"`) {
		t.Fatal("other lost")
	}
	if auth.Get() != "dial" {
		t.Fatalf("auth %q", auth.Get())
	}
	if err := auth.Set("dial"); err != nil {
		t.Fatal(err)
	}
	// second catalog pick must not overwrite the backup
	if err := model.Set("fake/m2"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	if v, _ := envVal(dir, "GEMINI_API_KEY"); v != "orig" {
		t.Fatalf("key %q", v)
	}
	if v, ok := envVal(dir, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatalf("base left: %q", v)
	}
	if setting(dir, "security.auth.selectedType") != "oauth-personal" || setting(dir, "model.name") != "gemini-2.5-flash" {
		t.Fatal("not restored")
	}
	if auth.Get() == "dial" {
		t.Fatal("still dial")
	}
	if len(stashLoad()) != 0 {
		t.Fatalf("stash left: %v", stashLoad())
	}
	_ = provider.Path
}

func TestGeminiDefaultRestoresAbsent(t *testing.T) {
	a, dir := geminiEnv(t)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("FOO=bar\n"), 0o600)
	model := a.Field("model")
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	if _, ok := envVal(dir, "GEMINI_API_KEY"); ok {
		t.Fatal("key left")
	}
	if _, ok := envVal(dir, "GOOGLE_GEMINI_BASE_URL"); ok {
		t.Fatal("base left")
	}
	if v, _ := envVal(dir, "FOO"); v != "bar" {
		t.Fatal("FOO lost")
	}
	if setting(dir, "security.auth.selectedType") != "" || setting(dir, "model.name") != "" {
		t.Fatal("settings left")
	}
}

func TestFieldByLabel(t *testing.T) {
	a, _ := geminiEnv(t)
	for _, k := range []string{"auth", "provider", "model"} {
		if a.Field(k) == nil {
			t.Fatal(k)
		}
	}
}
