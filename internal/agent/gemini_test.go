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

func geminiHome(t *testing.T) (string, *Agent) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=orig-key\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"security":{"auth":{"selectedType":"oauth-personal"}},"model":{"name":"gemini-2.5-pro"}}`), 0o600)
	return home, gemini(home)
}

func TestGeminiRouteAndRestore(t *testing.T) {
	for _, back := range []string{"gemini-2.5-flash", ""} {
		home, a := geminiHome(t)
		env := filepath.Join(home, ".gemini", ".env")
		set := filepath.Join(home, ".gemini", "settings.json")
		if err := a.Field("model").Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
		if v, _ := edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL"); v != gateway.URL() {
			t.Fatalf("base url %q", v)
		}
		if v, _ := edit.GetEnvFile(env, "GEMINI_API_KEY"); v != gateway.Token {
			t.Fatalf("key %q", v)
		}
		if v, _ := edit.GetJSON(set, "security.auth.selectedType"); v != "gemini-api-key" {
			t.Fatalf("auth %q", v)
		}
		if v, _ := edit.GetJSON(set, "model.name"); v != "fake/m1" {
			t.Fatalf("model %q", v)
		}
		if got := a.Field("auth").Get(); got != dialID {
			t.Fatalf("auth current %q", got)
		}
		if err := a.Field("auth").Set(dialID); err != nil {
			t.Fatal(err)
		}
		if err := a.Field("model").Set(back); err != nil {
			t.Fatal(err)
		}
		if v, _ := edit.GetEnvFile(env, "GEMINI_API_KEY"); back != "" && v != "orig-key" {
			t.Fatalf("key not restored: %q", v)
		}
		if v, ok := edit.GetEnvFile(env, "GOOGLE_GEMINI_BASE_URL"); ok && v != "" {
			t.Fatalf("base url left: %q", v)
		}
		if back == "" {
			if v, _ := edit.GetJSON(set, "model.name"); v != "" {
				t.Fatalf("model left %q", v)
			}
			if v, _ := edit.GetJSON(set, "security.auth.selectedType"); v != "" {
				t.Fatalf("auth left %q", v)
			}
			continue
		}
		if v, _ := edit.GetJSON(set, "security.auth.selectedType"); v != "oauth-personal" {
			t.Fatalf("auth not restored: %q", v)
		}
		if v, _ := edit.GetJSON(set, "model.name"); v != back {
			t.Fatalf("model %q", v)
		}
		if a.Field("auth").Get() == dialID {
			t.Fatal("still routed")
		}
	}
}

func TestGeminiRestoreDeletesAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "cfg"))
	provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}})
	a := gemini(home)
	if err := a.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(home, ".gemini", ".env")
	if v, _ := edit.GetEnvFile(env, "GEMINI_API_KEY"); v != "" {
		t.Fatalf("key left %q", v)
	}
	if v, _ := edit.GetJSON(filepath.Join(home, ".gemini", "settings.json"), "security.auth.selectedType"); v != "" {
		t.Fatalf("auth left %q", v)
	}
}

func TestGeminiAuthDialUnrouted(t *testing.T) {
	_, a := geminiHome(t)
	err := a.Field("auth").Set(dialID)
	if err == nil || !strings.Contains(err.Error(), "pick a model via dial instead; that routes Gemini CLI through the gateway") {
		t.Fatalf("err %v", err)
	}
	for _, o := range a.Field("auth").Options(nil) {
		if o.Value == dialID {
			t.Fatal("dial offered while unrouted")
		}
	}
}

func TestFieldByLabel(t *testing.T) {
	a := &Agent{Fields: []Field{{Key: "provider", Label: "auth"}, {Key: "model", Label: "model"}, {Key: "x", Label: "provider"}}}
	if f := a.Field("auth"); f == nil || f.Key != "provider" {
		t.Fatal("label lookup")
	}
	if f := a.Field("provider"); f == nil || f.Key != "provider" {
		t.Fatal("key must win over label")
	}
	if f := a.Field("model"); f == nil || f.Key != "model" {
		t.Fatal("model")
	}
	if a.Field("nope") != nil {
		t.Fatal("unexpected")
	}
}
