package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// A Gemini CLI routed through dial lists under the provider it routes to: its
// first field is the auth choice, so the provider comes from its own field.
func TestUsesByProviderCountsRoutedGemini(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))

	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k",
		Chat: "http://127.0.0.1:9/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"fake/m1"}}`
	env := "GOOGLE_GEMINI_BASE_URL=" + gateway.URL() + "\nGEMINI_API_KEY=" + gateway.Token + "\n"
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := usesByProvider()["fake"]; !reflect.DeepEqual(got, []string{"Gemini CLI"}) {
		t.Errorf("uses of fake = %v, want [Gemini CLI]", got)
	}
}
