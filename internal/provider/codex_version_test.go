package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// resetCodexVersion drops the cached answer and puts the CLI and clock back
// after a test that changed them.
func cverResetCodexVersion(t *testing.T, cli string) {
	t.Helper()
	codexVersions.Lock()
	codexVersions.v, codexVersions.at = "", time.Time{}
	codexVersions.Unlock()
	oldCLI, oldNow := codexCLIVersion, codexVersionNow
	codexCLIVersion = func() string { return cli }
	t.Cleanup(func() {
		codexCLIVersion, codexVersionNow = oldCLI, oldNow
		codexVersions.Lock()
		codexVersions.v, codexVersions.at = "", time.Time{}
		codexVersions.Unlock()
	})
}

// withCodexCache writes a models cache with the given client version as the
// home directory's.
func cverWithCodexCache(t *testing.T, version string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if version == "" {
		return
	}
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"client_version":"` + version + `","models":[]}`
	if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The newest of the CLI, the cache and the built-in default is asked with.
func TestCverCodexRequestVersionNewest(t *testing.T) {
	cverWithCodexCache(t, "0.154.0")
	cverResetCodexVersion(t, "codex-cli 0.155.1")
	if got := codexRequestVersion(); got != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache 0.154.0: got %q", got)
	}
}

// Without a Codex CLI the cache and the default are the choices.
func TestCverCodexRequestVersionNoCLI(t *testing.T) {
	cverWithCodexCache(t, "0.154.0")
	cverResetCodexVersion(t, "")
	if got := codexRequestVersion(); got != "0.154.0" {
		t.Fatalf("no CLI, cache 0.154.0: got %q", got)
	}
}

// With neither a CLI nor a cache, the built-in default is asked with, and an
// older cache doesn't win over a newer default.
func TestCverCodexRequestVersionDefault(t *testing.T) {
	cverWithCodexCache(t, "")
	cverResetCodexVersion(t, "")
	if got := codexRequestVersion(); got != codexClientVersion {
		t.Fatalf("nothing found: got %q, want %q", got, codexClientVersion)
	}
	cverWithCodexCache(t, "0.100.0")
	cverResetCodexVersion(t, "")
	if got := codexRequestVersion(); got != codexClientVersion {
		t.Fatalf("older cache: got %q, want %q", got, codexClientVersion)
	}
}

// The answer is kept for about ten minutes, not longer.
func TestCverCodexRequestVersionCache(t *testing.T) {
	cverWithCodexCache(t, "0.154.0")
	cverResetCodexVersion(t, "codex-cli 0.155.1")
	now := time.Now()
	codexVersionNow = func() time.Time { return now }
	if got := codexRequestVersion(); got != "0.155.1" {
		t.Fatalf("first: %q", got)
	}
	codexCLIVersion = func() string { return "codex-cli 0.156.0" }
	now = now.Add(9 * time.Minute)
	if got := codexRequestVersion(); got != "0.155.1" {
		t.Fatalf("within ten minutes, still kept: %q", got)
	}
	now = now.Add(2 * time.Minute)
	if got := codexRequestVersion(); got != "0.156.0" {
		t.Fatalf("after ten minutes, asked again: %q", got)
	}
}

func TestCverNewestCodexVersion(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"codex-cli 0.9.0", "0.10.0"}, "0.10.0"},
		{[]string{"", "", ""}, codexClientVersion},
		{[]string{"garbage", "0.200.1"}, "0.200.1"},
		{[]string{"1.2.3", "1.2.3"}, "1.2.3"},
	}
	for _, c := range cases {
		if got := newestCodexVersion(c.in...); got != c.want {
			t.Errorf("newestCodexVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
