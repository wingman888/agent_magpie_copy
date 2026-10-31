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

func TestAcceptGeminiCLIViaDial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"},"other":1}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=orig-key\nFOO=bar\n"), 0o600)

	a, err := agent.Find("gemini")
	if err != nil {
		t.Fatal(err)
	}
	model, auth := a.Field("model"), a.Field("auth")
	if model == nil || auth == nil || a.Field("provider") == nil {
		t.Fatalf("fields: model=%v auth=%v", model, auth)
	}
	if err := model.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	st, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if !strings.Contains(string(env), "GOOGLE_GEMINI_BASE_URL="+gateway.URL()) || !strings.Contains(string(env), "GEMINI_API_KEY="+gateway.Token) || !strings.Contains(string(env), "FOO=bar") {
		t.Fatalf(".env: %s", env)
	}
	if !strings.Contains(string(st), "gemini-api-key") || !strings.Contains(string(st), "fake/m1") {
		t.Fatalf("settings: %s", st)
	}
	a, _ = agent.Find("gemini")
	if got := a.Field("auth").Get(); got != "dial" {
		t.Fatalf("auth shows %q", got)
	}
	if err := a.Field("auth").Set("dial"); err != nil {
		t.Fatalf("already routed, selecting dial should be fine: %v", err)
	}

	// native model restores everything
	if err := a.Field("model").Set("gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	env, _ = os.ReadFile(filepath.Join(dir, ".env"))
	st, _ = os.ReadFile(filepath.Join(dir, "settings.json"))
	if strings.Contains(string(env), "GOOGLE_GEMINI_BASE_URL") || !strings.Contains(string(env), "GEMINI_API_KEY=orig-key") {
		t.Fatalf("restored .env: %s", env)
	}
	if !strings.Contains(string(st), "oauth-personal") || !strings.Contains(string(st), "gemini-2.5-flash") || !strings.Contains(string(st), `"other"`) {
		t.Fatalf("restored settings: %s", st)
	}
	if a, _ = agent.Find("gemini"); a.Field("auth").Get() == "dial" {
		t.Fatal("still dial")
	}

	// cannot hand-pick dial as auth when not routed
	if err := a.Field("auth").Set("dial"); err == nil {
		t.Fatal("auth=dial must be refused unless routed")
	}
}

func TestAcceptGeminiCLIDefaultClearsRouting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "http://127.0.0.1:1/v1", Key: "k", Models: []string{"m1"}})
	a, _ := agent.Find("gemini")
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	env, _ := os.ReadFile(filepath.Join(dir, ".env"))
	st, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if strings.Contains(string(env), "GOOGLE_GEMINI_BASE_URL") || strings.Contains(string(env), "GEMINI_API_KEY") ||
		strings.Contains(string(st), "gemini-api-key") || strings.Contains(string(st), "fake/m1") {
		t.Fatalf("not cleared: env=%s settings=%s", env, st)
	}
}
