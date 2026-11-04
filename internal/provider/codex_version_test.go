package provider

import (
	"os"
	"path/filepath"
	"testing"
)

// codexVersionAsked sets up a home whose Codex cache records cacheVer, and a
// Codex CLI reporting cliVer ("" for none), and returns the version asked for.
func codexVersionAsked(t *testing.T, cacheVer, cliVer string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if cacheVer != "" {
		dir := filepath.Join(home, ".codex")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"client_version":"` + cacheVer + `","models":[]}`
		if err := os.WriteFile(filepath.Join(dir, "models_cache.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldCLI := codexCLIVersion
	codexCLIVersion = func() string { return cliVer }
	forgetCodexVersion()
	t.Cleanup(func() {
		codexCLIVersion = oldCLI
		forgetCodexVersion()
	})
	return codexRequestVersion()
}

func TestCodexRequestVersionNewest(t *testing.T) {
	if got := codexVersionAsked(t, "0.154.0", "0.155.1"); got != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache 0.154.0: asked %q, want 0.155.1", got)
	}
	if got := codexVersionAsked(t, "0.156.0", "0.155.1"); got != "0.156.0" {
		t.Fatalf("CLI 0.155.1, cache 0.156.0: asked %q, want 0.156.0", got)
	}
}

func TestCodexRequestVersionWithoutCLI(t *testing.T) {
	if got := codexVersionAsked(t, "0.154.0", ""); got != "0.154.0" {
		t.Fatalf("no CLI, cache 0.154.0: asked %q, want 0.154.0", got)
	}
	if got := codexVersionAsked(t, "", ""); got != codexClientVersion {
		t.Fatalf("no CLI, no cache: asked %q, want %q", got, codexClientVersion)
	}
}

func TestCodexRequestVersionKeptForAWhile(t *testing.T) {
	codexVersionAsked(t, "0.154.0", "0.155.1")
	oldCLI := codexCLIVersion
	codexCLIVersion = func() string { return "0.999.0" }
	defer func() { codexCLIVersion = oldCLI }()
	if got := codexRequestVersion(); got != "0.155.1" {
		t.Fatalf("within the TTL the kept version is used, got %q", got)
	}
}

func TestNewerVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.155.1", "0.154.0", true},
		{"0.154.0", "0.155.1", false},
		{"1.0", "0.9.9", true},
		{"0.154.0", "0.154.0", false},
		{"", "0.154.0", false},
		{"0.154.0", "", true},
	}
	for _, c := range cases {
		if got := newerVersion(c.a, c.b); got != c.want {
			t.Errorf("newerVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if got := versionIn("codex-cli 0.155.1\n"); got != "0.155.1" {
		t.Errorf("versionIn = %q", got)
	}
}
