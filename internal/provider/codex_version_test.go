package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rgxWithCodexVersion sets up a home with Codex CLI's cache at cacheVersion
// ("" for none) and a Codex CLI that reports cliOut (nil for none installed).
func rgxWithCodexVersion(t *testing.T, cacheVersion string, cliOut []byte) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if cacheVersion != "" {
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(`{"client_version":"`+cacheVersion+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := codexVersionCommand
	codexVersionCommand = func(context.Context) ([]byte, error) {
		if cliOut == nil {
			return nil, errors.New("codex: not found")
		}
		return cliOut, nil
	}
	t.Cleanup(func() { codexVersionCommand = old })
	codexVersionCache.Lock()
	codexVersionCache.v, codexVersionCache.at = "", time.Time{}
	codexVersionCache.Unlock()
}

// The client version is the newest of the CLI, the cache and the built-in one.
func TestRgxCodexRequestVersionNewestWins(t *testing.T) {
	rgxWithCodexVersion(t, "0.154.0", []byte("codex-cli 0.155.1\n"))
	if got := codexRequestVersion(context.Background()); got != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache 0.154.0: asked with %q", got)
	}
}

func TestRgxCodexRequestVersionWithoutCLI(t *testing.T) {
	rgxWithCodexVersion(t, "0.154.0", nil)
	if got := codexRequestVersion(context.Background()); got != "0.154.0" {
		t.Fatalf("no CLI: asked with %q", got)
	}
}

func TestRgxCodexRequestVersionCacheNewerThanCLI(t *testing.T) {
	rgxWithCodexVersion(t, "0.156.0", []byte("codex-cli 0.155.1"))
	if got := codexRequestVersion(context.Background()); got != "0.156.0" {
		t.Fatalf("cache 0.156.0, CLI 0.155.1: asked with %q", got)
	}
}

func TestRgxCodexRequestVersionBuiltInDefault(t *testing.T) {
	rgxWithCodexVersion(t, "", nil)
	if got := codexRequestVersion(context.Background()); got != codexClientVersion {
		t.Fatalf("nothing found: asked with %q", got)
	}
}

// The answer is kept for a while, so the CLI isn't run on every list.
func TestRgxCodexRequestVersionCached(t *testing.T) {
	rgxWithCodexVersion(t, "0.154.0", []byte("codex-cli 0.155.1"))
	if got := codexRequestVersion(context.Background()); got != "0.155.1" {
		t.Fatalf("first: %q", got)
	}
	codexVersionCommand = func(context.Context) ([]byte, error) { return []byte("codex-cli 0.160.0"), nil }
	if got := codexRequestVersion(context.Background()); got != "0.155.1" {
		t.Fatalf("second, within the cache's life: %q", got)
	}
}

func TestRgxCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.155.1", "0.154.0", 1},
		{"0.154.0", "0.155.1", -1},
		{"1.0", "1.0.0", 0},
		{"0.10.0", "0.9.9", 1},
		{"junk", "0.1.0", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
