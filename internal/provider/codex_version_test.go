package provider

import (
	"testing"
	"time"
)

// withCodexVersions stands in for the machine's Codex CLI and cache, and
// starts the asked-for version over.
func withCodexVersions(t *testing.T, cli, cache string) {
	t.Helper()
	oldCLI, oldCache := codexCLIVersion, codexCacheVersion
	codexCLIVersion = func() string { return cli }
	codexCacheVersion = func() string { return cache }
	codexVersionAskedCache.Lock()
	codexVersionAskedCache.v, codexVersionAskedCache.at = "", time.Time{}
	codexVersionAskedCache.Unlock()
	t.Cleanup(func() {
		codexCLIVersion, codexCacheVersion = oldCLI, oldCache
		codexVersionAskedCache.Lock()
		codexVersionAskedCache.v, codexVersionAskedCache.at = "", time.Time{}
		codexVersionAskedCache.Unlock()
	})
}

func TestCodexVersionAskedIsTheNewest(t *testing.T) {
	withCodexVersions(t, "codex-cli 0.155.1", "0.154.0")
	if got := codexVersionAsked(); got != "0.155.1" {
		t.Fatalf("CLI 0.155.1, cache 0.154.0: asked %q", got)
	}

	withCodexVersions(t, "", "0.154.0")
	if got := codexVersionAsked(); got != "0.154.0" {
		t.Fatalf("no CLI, cache 0.154.0: asked %q", got)
	}

	withCodexVersions(t, "codex-cli 0.9.0", "0.10.0")
	if got := codexVersionAsked(); got != "0.10.0" {
		t.Fatalf("numbers compare as numbers: asked %q", got)
	}

	withCodexVersions(t, "", "")
	if got := codexVersionAsked(); got != codexClientVersion {
		t.Fatalf("nothing found: asked %q, want the built-in %q", got, codexClientVersion)
	}
}

func TestCodexVersionAskedIsCached(t *testing.T) {
	withCodexVersions(t, "codex-cli 0.155.1", "0.154.0")
	if got := codexVersionAsked(); got != "0.155.1" {
		t.Fatalf("asked %q", got)
	}
	codexCLIVersion = func() string { return "codex-cli 0.156.0" }
	if got := codexVersionAsked(); got != "0.155.1" {
		t.Fatalf("within ten minutes the answer is kept: asked %q", got)
	}
	codexVersionAskedCache.Lock()
	codexVersionAskedCache.at = time.Now().Add(-codexVersionTTL - time.Second)
	codexVersionAskedCache.Unlock()
	if got := codexVersionAsked(); got != "0.156.0" {
		t.Fatalf("after ten minutes it is asked again: asked %q", got)
	}
}
