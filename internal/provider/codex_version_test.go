package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func resetCodexVersion() {
	codexVersionMu.Lock()
	codexVersion, codexVersionAt = "", time.Time{}
	codexVersionMu.Unlock()
}

// codexAsked runs a models list with the Codex CLI reporting cli (or none),
// and Codex CLI's cache recording cache (or none), and says the client version
// the list was asked with.
func codexAsked(t *testing.T, cli func() (string, error), cache string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if cache != "" {
		if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"client_version":"` + cache + `"}`
		if err := os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldCLI := codexVersionFromCLI
	codexVersionFromCLI = cli
	resetCodexVersion()
	t.Cleanup(func() {
		codexVersionFromCLI = oldCLI
		resetCodexVersion()
	})

	var mu sync.Mutex
	var asked []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Query().Get("client_version"))
		mu.Unlock()
		w.Write([]byte(`{"models": [{"slug": "gpt-6-sol", "display_name": "GPT-6 Sol", "visibility": "list", "priority": 1}]}`))
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	noSign := func(context.Context, *http.Request, []byte) error { return nil }
	if ms, err := codexModels(context.Background(), noSign); err != nil || len(ms) != 1 {
		t.Fatalf("models: %v %+v", err, ms)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(asked) != 1 {
		t.Fatalf("asked %d times, want 1", len(asked))
	}
	return asked[0]
}

func cliReports(v string) func() (string, error) {
	return func() (string, error) { return v, nil }
}

func noCLI() (string, error) { return "", errors.New("codex: executable file not found") }

// The CLI is newer than the cache: the list is asked with the CLI's version.
func TestCodexModelsAskWithTheCLIVersion(t *testing.T) {
	if got := codexAsked(t, cliReports("0.155.1"), "0.154.0"); got != "0.155.1" {
		t.Fatalf("client_version = %q, want 0.155.1", got)
	}
}

// Without a CLI, the cache's version is asked with.
func TestCodexModelsWithoutCLIUseTheCache(t *testing.T) {
	if got := codexAsked(t, noCLI, "0.154.0"); got != "0.154.0" {
		t.Fatalf("client_version = %q, want 0.154.0", got)
	}
}

// Without a CLI and without a cache, the built-in default is asked with.
func TestCodexModelsWithoutAnythingUseTheDefault(t *testing.T) {
	if got := codexAsked(t, noCLI, ""); got != codexClientVersion {
		t.Fatalf("client_version = %q, want %s", got, codexClientVersion)
	}
}

// The newest of the three wins, whichever it is.
func TestCodexClientVersionIsTheNewest(t *testing.T) {
	if got := codexAsked(t, cliReports("0.150.0"), "0.156.0"); got != "0.156.0" {
		t.Fatalf("cache newer than CLI: %q", got)
	}
	if got := codexAsked(t, cliReports("0.100.0"), ""); got != codexClientVersion {
		t.Fatalf("default newer than CLI: %q", got)
	}
}

// The version resolved is kept for a while, not looked up again each time.
func TestCodexClientVersionIsCached(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldCLI := codexVersionFromCLI
	t.Cleanup(func() {
		codexVersionFromCLI = oldCLI
		resetCodexVersion()
	})
	resetCodexVersion()
	calls := 0
	codexVersionFromCLI = func() (string, error) {
		calls++
		return "0.155.1", nil
	}
	if got := codexClientVersionNow(); got != "0.155.1" {
		t.Fatalf("first: %q", got)
	}
	if got := codexClientVersionNow(); got != "0.155.1" || calls != 1 {
		t.Fatalf("second: %q, CLI asked %d times", got, calls)
	}
	codexVersionMu.Lock()
	codexVersionAt = time.Now().Add(-codexVersionTTL - time.Second)
	codexVersionMu.Unlock()
	if codexClientVersionNow(); calls != 2 {
		t.Fatalf("after the TTL the CLI is asked again: %d calls", calls)
	}
}
