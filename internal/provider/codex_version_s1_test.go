package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// The newest of the installed Codex CLI's version and Codex CLI's cache is
// asked with, never older than the built-in one.
func TestS1CodexClientVersionNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.154.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := codexVersionCLI
	t.Cleanup(func() { codexVersionCLI = old })

	codexVersionCLI = func() (string, bool) { return "0.155.1", true }
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache 0.154.0: asked with %s", got)
	}

	codexVersionCLI = func() (string, bool) { return "", false }
	if got := codexClientVersionNow(); got != "0.154.0" {
		t.Fatalf("no CLI: asked with %s", got)
	}

	// the cache is newer than the CLI here: the cache wins
	if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"client_version":"0.156.2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	codexVersionCLI = func() (string, bool) { return "0.155.1", true }
	if got := codexClientVersionNow(); got != "0.156.2" {
		t.Fatalf("cache 0.156.2, CLI 0.155.1: asked with %s", got)
	}
}

// Versions compare by their numbers, not as text: 0.10.0 is after 0.9.9.
func TestS1CodexVersionNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.155.1", "0.154.0", true},
		{"0.154.0", "0.155.1", false},
		{"0.10.0", "0.9.9", true},
		{"0.154", "0.154.0", false},
		{"1.0", "0.154.0", true},
		{"junk", "0.154.0", false},
	} {
		if got := codexVersionNewer(c.a, c.b); got != c.want {
			t.Errorf("codexVersionNewer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
