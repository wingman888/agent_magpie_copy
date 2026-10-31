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
	t.Helper()
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".gemini"), 0o755)
	a := gemini(home)
	return a, filepath.Join(home, ".gemini", "settings.json"), filepath.Join(home, ".gemini", ".env")
}

func gj(path, k string) string { v, _ := edit.GetJSON(path, k); return v }
func ge(path, k string) string { v, _ := edit.GetEnvFile(path, k); return v }

func TestGeminiRouteRestore(t *testing.T) {
	for _, back := range []string{"gemini-2.5-pro", ""} {
		a, settings, env := geminiEnv(t)
		os.WriteFile(settings, []byte("{\n  \"security\": {\"auth\": {\"selectedType\": \"oauth-personal\"}},\n  \"model\": {\"name\": \"gemini-2.5-flash\"},\n  \"theme\": \"x\"\n}\n"), 0o644)
		os.WriteFile(env, []byte("# my env\nGEMINI_API_KEY=mine\nGOOGLE_GEMINI_BASE_URL=https://example.com\nOTHER=1\n"), 0o644)
		model, auth := a.Field("model"), a.Field("auth")

		if err := model.Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if ge(env, "GOOGLE_GEMINI_BASE_URL") != gateway.URL() || ge(env, "GEMINI_API_KEY") != gateway.Token ||
			gj(settings, "security.auth.selectedType") != "gemini-api-key" || gj(settings, "model.name") != "fake/m1" {
			t.Fatalf("not routed: %s %s", mustRead(env), mustRead(settings))
		}
		if auth.Get() != "dial" {
			t.Fatalf("auth = %q", auth.Get())
		}
		// switching among catalog models keeps the first stash
		if err := model.Set("fake/other"); err != nil {
			t.Fatal(err)
		}
		if back != "" {
			if err := model.Set(back); err != nil {
				t.Fatal(err)
			}
		} else if err := model.Set(""); err != nil {
			t.Fatal(err)
		}
		if ge(env, "GOOGLE_GEMINI_BASE_URL") != "https://example.com" || ge(env, "GEMINI_API_KEY") != "mine" ||
			gj(settings, "security.auth.selectedType") != "oauth-personal" || ge(env, "OTHER") != "1" || gj(settings, "theme") != "x" {
			t.Fatalf("not restored: %s %s", mustRead(env), mustRead(settings))
		}
		want := back
		if back == "" {
			want = ""
		}
		if gj(settings, "model.name") != want {
			t.Fatalf("model = %q want %q", gj(settings, "model.name"), want)
		}
		if !strings.Contains(mustRead(env), "# my env") {
			t.Fatal("comment lost")
		}
	}
}

func TestGeminiRouteFromEmpty(t *testing.T) {
	a, settings, env := geminiEnv(t)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if ge(env, "GOOGLE_GEMINI_BASE_URL") != "" || ge(env, "GEMINI_API_KEY") != "" || gj(settings, "security.auth.selectedType") != "" {
		t.Fatalf("leftovers: %s %s", mustRead(env), mustRead(settings))
	}
	if gj(settings, "model.name") != "gemini-2.5-pro" || a.Field("auth").Get() != "google" {
		t.Fatal("bad final state")
	}
}

func TestGeminiAuthDial(t *testing.T) {
	a, _, env := geminiEnv(t)
	err := a.Field("auth").Set("dial")
	if err == nil || !strings.Contains(err.Error(), "dial model") {
		t.Fatalf("err = %v", err)
	}
	os.WriteFile(env, []byte("GEMINI_API_KEY=mine\n"), 0o644)
	a.Field("model").Set("fake/m1")
	if err := a.Field("auth").Set("google"); err != nil {
		t.Fatal(err)
	}
	if ge(env, "GEMINI_API_KEY") != "mine" || ge(env, "GOOGLE_GEMINI_BASE_URL") != "" || a.Field("auth").Get() != "google" {
		t.Fatal(mustRead(env))
	}
}

func TestFieldByLabel(t *testing.T) {
	a, _, _ := geminiEnv(t)
	if f := a.Field("AUTH"); f == nil || f.Key != "provider" {
		t.Fatal("label lookup failed")
	}
	if a.Field("provider") == nil || a.Field("model") == nil || a.Field("nope") != nil {
		t.Fatal("key lookup broken")
	}
}

func mustRead(p string) string { b, _ := os.ReadFile(p); return string(b) }
