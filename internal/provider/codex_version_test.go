package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The models list is asked with the newest of the installed Codex CLI, the
// cache's and the default version; without a CLI, the cache's and the default's.
func TestCodexClientVersionIsNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), map[string]any{"client_version": "0.154.0"})
	old := codexCLIVersion
	t.Cleanup(func() { codexCLIVersion = old; forgetCodexVersion() })

	codexCLIVersion = func() string { return "0.155.1" }
	forgetCodexVersion()
	if v := codexClientVersionNow(); v != "0.155.1" {
		t.Fatalf("with the CLI at 0.155.1: %s", v)
	}

	// the answer is kept for a while
	codexCLIVersion = func() string { return "0.156.0" }
	if v := codexClientVersionNow(); v != "0.155.1" {
		t.Fatalf("not kept: %s", v)
	}

	codexCLIVersion = func() string { return "" }
	forgetCodexVersion()
	if v := codexClientVersionNow(); v != "0.154.0" {
		t.Fatalf("without the CLI: %s", v)
	}
}

// The built-in default is the floor when the cache records an older version.
func TestCodexClientVersionDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := codexCLIVersion
	t.Cleanup(func() { codexCLIVersion = old; forgetCodexVersion() })
	codexCLIVersion = func() string { return "" }
	forgetCodexVersion()
	if v := codexClientVersionNow(); v != codexClientVersion {
		t.Fatalf("no cache, no CLI: %s", v)
	}
}

// `codex --version` is read from the codex on PATH.
func TestInstalledCodexVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for codex")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'codex-cli 0.155.1'\n"
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if v := installedCodexVersion(); v != "0.155.1" {
		t.Fatalf("installed: %q", v)
	}
	t.Setenv("PATH", t.TempDir())
	if v := installedCodexVersion(); v != "" {
		t.Fatalf("no codex on PATH: %q", v)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"0.155.1", "0.154.0", 1},
		{"0.10.0", "0.9.0", 1},
		{"1.0", "1.0.0", 0},
		{"0.154.0", "0.155.1", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
