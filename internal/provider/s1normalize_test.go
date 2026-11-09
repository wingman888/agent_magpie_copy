package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestS1NormalizeModelKey(t *testing.T) {
	same := []string{"claude-opus-5-5", "anthropic/claude-opus-5.5", "Claude-Opus-5.5", "claude-opus-5-5-20260801", "anthropic/Claude-Opus-5.5-20260801"}
	for _, m := range same {
		if got := NormalizeModelKey(m); got != "claude-opus-5-5" {
			t.Errorf("NormalizeModelKey(%q) = %q, want claude-opus-5-5", m, got)
		}
	}
	if a, b := NormalizeModelKey("x:batch"), NormalizeModelKey("x"); a == b {
		t.Errorf("a colon variant was folded into its base: %q and %q", a, b)
	}
	if got := NormalizeModelKey("x:batch"); got != "x:batch" {
		t.Errorf("x:batch = %q", got)
	}
	if got := NormalizeModelKey("v1.beta"); got != "v1.beta" {
		t.Errorf("v1.beta = %q, want unchanged", got)
	}
	if got := NormalizeModelKey("gpt-4.1"); got != "gpt-4-1" {
		t.Errorf("gpt-4.1 = %q", got)
	}
}

func TestS1CodexVersionIsTheNewest(t *testing.T) {
	oldCLI, oldCache := codexCLIVersion, codexCacheVersion
	t.Cleanup(func() {
		codexCLIVersion, codexCacheVersion = oldCLI, oldCache
		codexVersions.Lock()
		codexVersions.v, codexVersions.at = "", time.Time{}
		codexVersions.Unlock()
	})
	reset := func() {
		codexVersions.Lock()
		codexVersions.v, codexVersions.at = "", time.Time{}
		codexVersions.Unlock()
	}

	codexCLIVersion = func() string { return "0.155.1" }
	codexCacheVersion = func() string { return "0.154.0" }
	reset()
	if got := codexVersionNow(); got != "0.155.1" {
		t.Errorf("CLI 0.155.1, cache 0.154.0: asked with %q", got)
	}

	// kept for a while: a changed CLI is not asked again at once
	codexCLIVersion = func() string { return "0.156.0" }
	if got := codexVersionNow(); got != "0.155.1" {
		t.Errorf("within the TTL: %q", got)
	}

	codexCLIVersion = func() string { return "" }
	reset()
	if got := codexVersionNow(); got != "0.154.0" {
		t.Errorf("no CLI: asked with %q", got)
	}

	codexCacheVersion = func() string { return "" }
	reset()
	if got := codexVersionNow(); got != codexClientVersion {
		t.Errorf("no CLI, no cache: asked with %q", got)
	}

	if !codexVersionNewer("0.10.0", "0.9.9") || codexVersionNewer("0.154.0", "0.155.1") {
		t.Error("version comparison is not numeric")
	}
}

func TestS1CodexModelsAskWithNewest(t *testing.T) {
	var got string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query().Get("client_version")
		w.Write([]byte(`{"models": [{"slug": "gpt-6-sol", "display_name": "GPT-6 Sol", "visibility": "list", "priority": 1}]}`))
	}))
	defer fake.Close()
	oldBase, oldCLI, oldCache := CodexBase, codexCLIVersion, codexCacheVersion
	CodexBase = fake.URL + "/backend-api/codex"
	codexCLIVersion = func() string { return "0.155.1" }
	codexCacheVersion = func() string { return "0.154.0" }
	codexVersions.Lock()
	codexVersions.v, codexVersions.at = "", time.Time{}
	codexVersions.Unlock()
	t.Cleanup(func() {
		CodexBase, codexCLIVersion, codexCacheVersion = oldBase, oldCLI, oldCache
		codexVersions.Lock()
		codexVersions.v, codexVersions.at = "", time.Time{}
		codexVersions.Unlock()
	})
	sign := func(ctx context.Context, r *http.Request, b []byte) error { return nil }
	if _, err := codexModels(context.Background(), sign); err != nil {
		t.Fatal(err)
	}
	if got != "0.155.1" {
		t.Errorf("models asked with client_version %q, want 0.155.1", got)
	}
}
