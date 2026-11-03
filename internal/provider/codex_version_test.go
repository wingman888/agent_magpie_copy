package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// f09ResetCodexVersion empties the version cache now and again after the test.
func f09ResetCodexVersion(t *testing.T) {
	t.Helper()
	empty := func() {
		codexVersionCache.Lock()
		codexVersionCache.at, codexVersionCache.ver = time.Time{}, ""
		codexVersionCache.Unlock()
	}
	empty()
	t.Cleanup(empty)
}

// f09FakeCodexCLI makes an executable that prints the given version output and
// points codexExecutable at it.
func f09FakeCodexCLI(t *testing.T, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a shell script")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	script := "#!/bin/sh\necho '" + out + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := codexExecutable
	codexExecutable = func() string { return path }
	t.Cleanup(func() { codexExecutable = old })
}

// f09NoCodexCLI makes the installed CLI look absent.
func f09NoCodexCLI(t *testing.T) {
	t.Helper()
	old := codexExecutable
	codexExecutable = func() string { return "" }
	t.Cleanup(func() { codexExecutable = old })
}

// f09SetCodexHome points HOME at a fresh directory, and writes Codex CLI's
// models_cache.json with the given client version when it is not empty.
func f09SetCodexHome(t *testing.T, cacheVersion string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if cacheVersion == "" {
		return
	}
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"client_version": "` + cacheVersion + `", "models": []}`
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The newest of the installed CLI and its cache is asked with: CLI 0.155.1
// over a cache of 0.154.0.
func TestF09CodexVersionPrefersNewerInstalledCLI(t *testing.T) {
	f09ResetCodexVersion(t)
	f09SetCodexHome(t, "0.154.0")
	f09FakeCodexCLI(t, "codex-cli 0.155.1")
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("client version = %q, want 0.155.1", got)
	}
}

// Without the CLI, only the cache's version and the built-in one are
// candidates; here both are 0.154.0.
func TestF09CodexVersionWithoutCLI(t *testing.T) {
	f09ResetCodexVersion(t)
	f09SetCodexHome(t, "0.154.0")
	f09NoCodexCLI(t)
	if got := codexClientVersionNow(); got != "0.154.0" {
		t.Fatalf("client version = %q, want 0.154.0", got)
	}
}

// Without the CLI and without a cache, the built-in version is asked with.
func TestF09CodexVersionBuiltinFallback(t *testing.T) {
	f09ResetCodexVersion(t)
	f09SetCodexHome(t, "")
	f09NoCodexCLI(t)
	if got := codexClientVersionNow(); got != codexClientVersion {
		t.Fatalf("client version = %q, want %q", got, codexClientVersion)
	}
}

// Versions compare numerically, not as text: 0.156.0 beats 0.155.10 and
// 0.155.10 beats 0.155.9.
func TestF09CodexVersionNumericComparison(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.155.1", "0.154.0", 1},
		{"0.154.0", "0.155.1", -1},
		{"0.155.10", "0.155.9", 1},
		{"1.0.0", "0.999.999", 1},
		{"0.154.0", "0.154.0", 0},
	}
	for _, c := range cases {
		if got := compareCodexVersion(c.a, c.b); got != c.want {
			t.Errorf("compareCodexVersion(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if got := newestCodexVersion("0.155.9", "0.155.10", "garbage", ""); got != "0.155.10" {
		t.Errorf("newestCodexVersion = %q, want 0.155.10", got)
	}
	if got := newestCodexVersion("", "x"); got != codexClientVersion {
		t.Errorf("newestCodexVersion with no valid candidate = %q, want built-in", got)
	}
}

// The choice is kept for about ten minutes: the CLI is not asked again in
// that time.
func TestF09CodexVersionIsCached(t *testing.T) {
	f09ResetCodexVersion(t)
	f09SetCodexHome(t, "0.154.0")
	f09FakeCodexCLI(t, "codex-cli 0.155.1")
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("first call = %q", got)
	}
	f09NoCodexCLI(t)
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("second call within the cache window = %q, want 0.155.1", got)
	}
	codexVersionCache.Lock()
	codexVersionCache.at = time.Now().Add(-11 * time.Minute)
	codexVersionCache.Unlock()
	if got := codexClientVersionNow(); got != "0.154.0" {
		t.Fatalf("after the cache window = %q, want 0.154.0", got)
	}
}
