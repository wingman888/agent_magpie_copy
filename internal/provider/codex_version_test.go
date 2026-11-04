package provider

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// isolateCodexVersion points HOME at a fresh directory holding a Codex cache
// of cacheVersion (none when empty), fakes the installed CLI as cliOut (no CLI
// when nil), and clears the ten-minute cache of the version asked.
func isolateCodexVersion(t *testing.T, cacheVersion string, cliOut []byte) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if cacheVersion != "" {
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(map[string]any{"client_version": cacheVersion, "models": []any{}})
		if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldLook, oldOut := codexLookPath, codexVersionOutput
	t.Cleanup(func() {
		codexLookPath, codexVersionOutput = oldLook, oldOut
		codexVersionCache.Lock()
		codexVersionCache.at, codexVersionCache.v = time.Time{}, ""
		codexVersionCache.Unlock()
	})
	codexLookPath = func(string) (string, error) {
		if cliOut == nil {
			return "", errors.New("not found")
		}
		return "/usr/bin/codex", nil
	}
	codexVersionOutput = func(string) ([]byte, error) { return cliOut, nil }
	codexVersionCache.Lock()
	codexVersionCache.at, codexVersionCache.v = time.Time{}, ""
	codexVersionCache.Unlock()
}

// The newest of the installed CLI, Codex's cache and the built-in version wins.
func TestCodexVersionNewestOfThree(t *testing.T) {
	isolateCodexVersion(t, "0.154.0", []byte("codex-cli 0.155.1\n"))
	if got := codexVersionToAsk(); got != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache 0.154.0: asked with %s", got)
	}
}

// Without a CLI the cache's version is asked with.
func TestCodexVersionWithoutCLI(t *testing.T) {
	isolateCodexVersion(t, "0.154.0", nil)
	if got := codexVersionToAsk(); got != "0.154.0" {
		t.Fatalf("no CLI, cache 0.154.0: asked with %s", got)
	}
}

// Without a cache or a CLI the built-in version is asked with, and a cache
// newer than the built-in one is not held back by it.
func TestCodexVersionFallbacks(t *testing.T) {
	isolateCodexVersion(t, "", nil)
	if got := codexVersionToAsk(); got != codexClientVersion {
		t.Fatalf("nothing found: asked with %s", got)
	}
	isolateCodexVersion(t, "0.160.0", nil)
	if got := codexVersionToAsk(); got != "0.160.0" {
		t.Fatalf("cache 0.160.0: asked with %s", got)
	}
}

// The version is kept ten minutes: a CLI updated in the meantime is seen after.
func TestCodexVersionCached(t *testing.T) {
	isolateCodexVersion(t, "0.154.0", []byte("codex-cli 0.155.1"))
	if got := codexVersionToAsk(); got != "0.155.1" {
		t.Fatalf("first ask: %s", got)
	}
	codexVersionOutput = func(string) ([]byte, error) { return []byte("codex-cli 0.156.0"), nil }
	if got := codexVersionToAsk(); got != "0.155.1" {
		t.Fatalf("within ten minutes the kept version is asked: %s", got)
	}
	codexVersionCache.Lock()
	codexVersionCache.at = time.Now().Add(-11 * time.Minute)
	codexVersionCache.Unlock()
	if got := codexVersionToAsk(); got != "0.156.0" {
		t.Fatalf("after ten minutes it is asked again: %s", got)
	}
}

func TestCompareCodexVersion(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.155.1", "0.154.0", 1},
		{"0.154.0", "0.155.1", -1},
		{"0.10.0", "0.9.9", 1},
		{"1.0", "1.0.0", 0},
	} {
		if got := compareCodexVersion(c.a, c.b); got != c.want {
			t.Errorf("compareCodexVersion(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
