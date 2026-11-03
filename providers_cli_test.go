package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/gateway"
	"github.com/yetone/dial/internal/provider"
)

// stdoutOf runs fn and returns what it printed to stdout.
func stdoutOf(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

// With no providers at all, `dial providers` still says that a signed-in
// Claude Code is not offered, and why.
func TestProvidersShowsClaudeNoteWithNoProviders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg-config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xdg-cache"))
	claude := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, ".credentials.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if n := len(provider.All()); n != 0 {
		t.Fatalf("expected no providers, got %d", n)
	}

	var err error
	out := stdoutOf(t, func() { err = providers() })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已登录但不提供") {
		t.Errorf("output lacks the Claude note: %q", out)
	}
	if !strings.Contains(out, "no providers yet") {
		t.Errorf("output lacks the empty-list hint: %q", out)
	}
}

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
