package provider

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// codexVersionCase sets up a home holding Codex's cache at cacheVersion (empty
// for no cache) and a stand-in CLI reporting cliOut (or failing when cliErr).
// The version cache is reset so each case computes afresh.
func codexVersionCase(t *testing.T, cacheVersion, cliOut string, cliErr error) (calls *int) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if cacheVersion != "" {
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"client_version": "` + cacheVersion + `", "models": []}`
		if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	oldProbe := codexCLIVersionProbe
	codexCLIVersionProbe = func() (string, error) {
		n++
		return cliOut, cliErr
	}
	codexVersionMu.Lock()
	oldAt, oldVal := codexVersionAt, codexVersionVal
	codexVersionAt, codexVersionVal = time.Time{}, ""
	codexVersionMu.Unlock()
	t.Cleanup(func() {
		codexCLIVersionProbe = oldProbe
		codexVersionMu.Lock()
		codexVersionAt, codexVersionVal = oldAt, oldVal
		codexVersionMu.Unlock()
	})
	return &n
}

// The CLI is newer than the cache: the CLI's version is asked with.
func TestCodexRequestVersionNewestOfCLIAndCache(t *testing.T) {
	codexVersionCase(t, "0.154.0", "codex-cli 0.155.1\n", nil)
	if got := codexRequestVersion(); got != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache 0.154.0: got %q, want 0.155.1", got)
	}
}

// No CLI: the cache's version (the newer of cache and built-in) is used.
func TestCodexRequestVersionWithoutCLI(t *testing.T) {
	codexVersionCase(t, "0.154.0", "", errors.New("exec: codex: not found"))
	if got := codexRequestVersion(); got != "0.154.0" {
		t.Fatalf("no CLI, cache 0.154.0: got %q, want 0.154.0", got)
	}
}

// No CLI and no cache: the built-in default is used.
func TestCodexRequestVersionBuiltinDefault(t *testing.T) {
	codexVersionCase(t, "", "", errors.New("exec: codex: not found"))
	if got := codexRequestVersion(); got != codexClientVersion {
		t.Fatalf("no CLI, no cache: got %q, want %q", got, codexClientVersion)
	}
}

// A cache newer than the CLI wins; the built-in default never goes backwards.
func TestCodexRequestVersionNotOlderThanDefault(t *testing.T) {
	codexVersionCase(t, "0.100.0", "codex-cli 0.120.0", nil)
	if got := codexRequestVersion(); got != codexClientVersion {
		t.Fatalf("older CLI and cache: got %q, want built-in %q", got, codexClientVersion)
	}
}

// The answer is cached: a second call within the window does not probe again.
func TestCodexRequestVersionCached(t *testing.T) {
	calls := codexVersionCase(t, "0.154.0", "codex-cli 0.155.1", nil)
	codexRequestVersion()
	codexRequestVersion()
	if *calls != 1 {
		t.Fatalf("CLI probed %d times within 10 minutes, want 1", *calls)
	}
}

func TestCompareCodexSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.155.1", "0.154.0", 1},
		{"0.154.0", "0.155.1", -1},
		{"0.154.0", "0.154.0", 0},
		{"1.0.0", "0.999.999", 1},
		{"0.10.0", "0.9.0", 1},
	}
	for _, c := range cases {
		if got := compareCodexSemver(c.a, c.b); got != c.want {
			t.Errorf("compareCodexSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
