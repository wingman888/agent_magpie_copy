package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

func capture(t *testing.T, fn func() error) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProvidersListAccounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{}`), 0o600)
	os.MkdirAll(filepath.Join(home, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "github-copilot", "hosts.json"), []byte(`{"github.com":{"user":"octocat","oauth_token":"t"}}`), 0o600)
	out := capture(t, providers)
	if !strings.Contains(out, "signed in as octocat") || strings.Contains(out, "custom") ||
		!strings.Contains(out, "Claude Code is signed in but not offered: ") {
		t.Fatalf("%s", out)
	}
	p, _ := provider.Find("copilot")
	det := capture(t, func() error { return showProvider(*p) })
	if !strings.Contains(det, "account") || !strings.Contains(det, "octocat") || !strings.Contains(det, "Copilot") {
		t.Fatalf("%s", det)
	}
}
