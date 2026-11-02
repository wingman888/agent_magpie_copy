package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A ChatGPT account lists the models of its own plan, asked with its own
// token; the cache Codex CLI keeps may be another account's.
func TestCodexModelsOfTheAccount(t *testing.T) {
	signIn(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/models" || r.Header.Get("chatgpt-account-id") != "acct-1" || r.URL.Query().Get("client_version") == "" {
			w.WriteHeader(400)
			return
		}
		w.Write([]byte(`{"models": [
			{"slug": "gpt-5.6-luna", "display_name": "GPT-5.6 Luna", "visibility": "list", "priority": 2,
			 "supported_reasoning_levels": [{"effort": "low"}, {"effort": "medium"}]},
			{"slug": "codex-auto-review", "visibility": "hide", "priority": 1}]}`))
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	p, ok := find(All(), "codex")
	if !ok {
		t.Fatal("no codex account")
	}
	if ms := p.Available(); len(ms) != 1 || ms[0].ID != "gpt-5.5" {
		t.Fatalf("before fetching, Codex CLI's cache: %+v", ms)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	ms := p.Available()
	if len(ms) != 1 || ms[0].ID != "gpt-5.6-luna" || len(ms[0].Efforts) != 2 {
		t.Fatalf("after fetching: %+v", ms)
	}
}

// The models list is asked for with the newest Codex known: the CLI
// installed over the version its cache was written with.
func TestCodexVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), map[string]any{"client_version": "0.154.0"})
	exe := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho codex-cli 0.155.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := codexExecutable
	t.Cleanup(func() { codexExecutable = old; codexVersionCache.at = time.Time{} })
	for _, c := range []struct{ exe, want string }{{exe, "0.155.1"}, {"", "0.154.0"}} {
		codexExecutable = func() string { return c.exe }
		codexVersionCache.at = time.Time{}
		if got := codexVersion(); got != c.want {
			t.Errorf("with %q: %s, want %s", c.exe, got, c.want)
		}
	}
}
