package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/agent"
	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

func geminiHome(t *testing.T) (*agent.Agent, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DIAL_ADDR", "127.0.0.1:39425")
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find("gemini")
	if err != nil {
		t.Fatal(err)
	}
	return a, filepath.Join(home, ".gemini")
}

func readF(t *testing.T, p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestAcceptGeminiCLIRouting(t *testing.T) {
	a, dir := geminiHome(t)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=orig-key\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`), 0o600)

	auth, model := a.Field("auth"), a.Field("model")
	if auth == nil || model == nil {
		t.Fatal("fields missing")
	}
	if a.Field("auth") != auth || a.Field("model").Label != "model" {
		t.Error("lookup")
	}
	if err := auth.Set("dial"); err == nil || err.Error() != "pick a model via dial instead; that routes Gemini CLI through the gateway" {
		t.Errorf("auth dial err: %v", err)
	}
	for _, o := range auth.Options(nil) {
		if o.Value == "dial" {
			t.Error("dial offered while not routed")
		}
	}

	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	env := readF(t, filepath.Join(dir, ".env"))
	if !strings.Contains(env, "GOOGLE_GEMINI_BASE_URL="+gateway.URL()) || !strings.Contains(env, "GEMINI_API_KEY="+gateway.Token) || strings.Contains(env, "orig-key") {
		t.Errorf("env: %s", env)
	}
	st := readF(t, filepath.Join(dir, "settings.json"))
	if !strings.Contains(st, `"gemini-api-key"`) || !strings.Contains(st, `"fake/m1"`) {
		t.Errorf("settings: %s", st)
	}
	if auth.Get() != "dial" || model.Get() != "fake/m1" {
		t.Errorf("get: %q %q", auth.Get(), model.Get())
	}
	found := false
	for _, o := range auth.Options(nil) {
		found = found || o.Value == "dial"
	}
	if !found {
		t.Error("dial option missing while routed")
	}
	if err := auth.Set("dial"); err != nil {
		t.Errorf("auth dial while routed: %v", err)
	}
	vd := false
	for _, o := range model.Options(nil) {
		vd = vd || o.Value == "fake/m1"
	}
	if !vd {
		t.Error("catalog model not offered")
	}

	// back to a native model: restore
	if err := model.Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	env = readF(t, filepath.Join(dir, ".env"))
	if strings.Contains(env, "GOOGLE_GEMINI_BASE_URL") || !strings.Contains(env, "GEMINI_API_KEY=orig-key") {
		t.Errorf("env after restore: %s", env)
	}
	if model.Get() != "gemini-2.5-flash" || auth.Get() == "dial" {
		t.Errorf("after restore: %q %q", model.Get(), auth.Get())
	}
	if !strings.Contains(readF(t, filepath.Join(dir, "settings.json")), "gemini-api-key") {
		t.Error("auth not restored")
	}
}

func TestAcceptGeminiCLIDefaultAndEmptyRestore(t *testing.T) {
	a, dir := geminiHome(t)
	model := a.Field("model")
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set(""); err != nil {
		t.Fatal(err)
	}
	env := readF(t, filepath.Join(dir, ".env"))
	st := readF(t, filepath.Join(dir, "settings.json"))
	if strings.Contains(env, "GOOGLE_GEMINI_BASE_URL") || strings.Contains(env, "GEMINI_API_KEY") ||
		strings.Contains(st, "selectedType") || strings.Contains(st, "model") && strings.Contains(st, "name") {
		t.Errorf("not cleaned: env=%q settings=%q", env, st)
	}
	if a.Field("auth").Get() == "dial" || model.Get() != "" {
		t.Error("still routed")
	}

	// from a pre-existing native config, route then switch to native: originally-empty items vanish
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := model.Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	env = readF(t, filepath.Join(dir, ".env"))
	st = readF(t, filepath.Join(dir, "settings.json"))
	if strings.Contains(env, "GOOGLE_GEMINI_BASE_URL") || strings.Contains(env, "GEMINI_API_KEY") || strings.Contains(st, "selectedType") {
		t.Errorf("leftovers: env=%q settings=%q", env, st)
	}
}
