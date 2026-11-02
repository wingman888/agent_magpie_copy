package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
