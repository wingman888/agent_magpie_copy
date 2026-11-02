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
	r, w, _ := os.Pipe()
	old := os.Stdout
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

func cliHome(t *testing.T) string {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(h, "claude"))
	os.MkdirAll(filepath.Join(h, ".config", "github-copilot"), 0o755)
	os.WriteFile(filepath.Join(h, ".config", "github-copilot", "hosts.json"), []byte(`{"github.com":{"user":"octocat","oauth_token":"gho"}}`), 0o600)
	return h
}

func TestProvidersListAccounts(t *testing.T) {
	h := cliHome(t)
	out := capture(t, providers)
	if !strings.Contains(out, "signed in as octocat") {
		t.Errorf("list: %s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "octocat") && strings.Contains(line, "custom") {
			t.Errorf("account marked custom: %s", line)
		}
	}
	if strings.Contains(out, "not offered") {
		t.Errorf("no claude sign-in, yet: %s", out)
	}
	os.MkdirAll(filepath.Join(h, "claude"), 0o755)
	os.WriteFile(filepath.Join(h, "claude", ".credentials.json"), []byte(`{}`), 0o600)
	out = capture(t, providers)
	if !strings.Contains(out, "Claude Code is signed in but not offered: ") {
		t.Errorf("list: %s", out)
	}
	p, err := provider.Find("copilot")
	if err != nil {
		t.Fatal(err)
	}
	out = capture(t, func() error { return showProvider(*p) })
	if !strings.Contains(out, "account") || !strings.Contains(out, "octocat") || !strings.Contains(out, "Copilot") || strings.Contains(out, "custom") {
		t.Errorf("show: %s", out)
	}
}
