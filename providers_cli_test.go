package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func capture(t *testing.T, f func() error) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProvidersListAccountsAndExcluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	d := filepath.Join(home, ".config", "github-copilot")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, "apps.json"), []byte(`{"github.com:Iv1.x":{"oauth_token":"gho_x","user":"octo"}}`), 0o600)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte("{}"), 0o600)

	out := capture(t, providers)
	if !strings.Contains(out, "signed in as octo") {
		t.Fatal(out)
	}
	if strings.Contains(out, "custom") {
		t.Fatal("account provider marked custom:\n" + out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if last := lines[len(lines)-1]; !strings.Contains(last, "Claude Code is signed in but not offered: ") {
		t.Fatal("last line:", last)
	}
}
