package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// xrWithCodexVersions stands in for the Codex CLI's version (cli, "" for no
// CLI) and for the models cache's (cache, "" for none), and clears the
// cached answer, for the length of the test.
func xrWithCodexVersions(t *testing.T, cli, cache string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "models_cache.json")
	if cache != "" {
		if err := os.WriteFile(path, []byte(`{"client_version":"`+cache+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldPath, oldCLI := codexCachePath, codexCLIOutput
	codexCachePath = func() string { return path }
	codexCLIOutput = func(context.Context) ([]byte, error) {
		if cli == "" {
			return nil, errors.New("codex: not found")
		}
		return []byte("codex-cli " + cli + "\n"), nil
	}
	codexVersionCache.Lock()
	codexVersionCache.v, codexVersionCache.at = "", time.Time{}
	codexVersionCache.Unlock()
	t.Cleanup(func() {
		codexCachePath, codexCLIOutput = oldPath, oldCLI
		codexVersionCache.Lock()
		codexVersionCache.v, codexVersionCache.at = "", time.Time{}
		codexVersionCache.Unlock()
	})
}

func TestXRCodexVersionPicksNewest(t *testing.T) {
	ctx := context.Background()

	xrWithCodexVersions(t, "0.155.1", "0.154.0")
	if got := codexVersion(ctx); got != "0.155.1" {
		t.Errorf("CLI 0.155.1, cache 0.154.0: got %s", got)
	}

	xrWithCodexVersions(t, "", "0.154.0")
	if got := codexVersion(ctx); got != "0.154.0" {
		t.Errorf("no CLI, cache 0.154.0: got %s", got)
	}

	xrWithCodexVersions(t, "", "")
	if got := codexVersion(ctx); got != codexClientVersion {
		t.Errorf("no CLI, no cache: got %s, want built-in %s", got, codexClientVersion)
	}

	xrWithCodexVersions(t, "0.150.0", "0.154.0")
	if got := codexVersion(ctx); got != "0.154.0" {
		t.Errorf("older CLI: got %s", got)
	}
}

func TestXRCodexVersionIsCached(t *testing.T) {
	ctx := context.Background()
	xrWithCodexVersions(t, "0.155.1", "")
	if got := codexVersion(ctx); got != "0.155.1" {
		t.Fatalf("first: %s", got)
	}
	calls := 0
	old := codexCLIOutput
	codexCLIOutput = func(ctx context.Context) ([]byte, error) {
		calls++
		return old(ctx)
	}
	if got := codexVersion(ctx); got != "0.155.1" || calls != 0 {
		t.Errorf("within the TTL: got %s, CLI run %d times", got, calls)
	}
	codexVersionCache.Lock()
	codexVersionCache.at = time.Now().Add(-codexVersionTTL - time.Second)
	codexVersionCache.Unlock()
	codexVersion(ctx)
	if calls != 1 {
		t.Errorf("after the TTL the CLI is asked again: %d", calls)
	}
}

func TestXRCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.155.1", "0.154.0", 1},
		{"0.154.0", "0.155.1", -1},
		{"1.0", "1.0.0", 0},
		{"0.10.0", "0.9.9", 1},
		{"2.0", "10.0", -1},
	} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if v := firstVersion("codex-cli 0.155.1 (build 7)"); v != "0.155.1" {
		t.Errorf("firstVersion: %q", v)
	}
}
