package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestProviderAPIsFromLiveList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "apis-prov", Name: "X", Chat: "https://x.test/v1", Responses: "https://x.test/v1"}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic", "chat", "gemini", "bogus"}},
		{ID: "unknown-apis"},
		{ID: "empty-apis", APIs: []string{}},
	}); err != nil {
		t.Fatal(err)
	}

	if got := fmt.Sprint(p.APIs("gpt-x")); got != "[responses]" {
		t.Fatalf("gpt-x: %s", got)
	}
	// declared order kept, duplicates and non-wire values dropped
	if got := fmt.Sprint(p.APIs("claude-x")); got != "[chat anthropic]" {
		t.Fatalf("claude-x: %s", got)
	}
	if got := p.APIs("unknown-apis"); got != nil {
		t.Fatalf("no APIs should be unknown: %v", got)
	}
	if got := p.APIs("empty-apis"); got != nil {
		t.Fatalf("empty APIs should be unknown: %v", got)
	}
	if got := p.APIs("not-listed"); got != nil {
		t.Fatalf("model not in the list: %v", got)
	}
}

func TestProviderAPIsWithoutList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "never-fetched", Chat: "https://x.test/v1"}
	if got := p.APIs("gpt-x"); got != nil {
		t.Fatalf("no list yet: %v", got)
	}
}

// Copilot declares all three endpoints, and its model list says which of
// them serve each model; the answer survives the cache.
func TestCopilotDeclaresEndpointsAndReadsSupported(t *testing.T) {
	signIn(t)
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-6-sol","name":"GPT 6 Sol","model_picker_enabled":true,"capabilities":{"type":"chat"},
			   "supported_endpoints":["/responses"]},
			  {"id":"claude-sonnet-5","name":"Claude Sonnet 5","model_picker_enabled":true,"capabilities":{"type":"chat"},
			   "supported_endpoints":["/chat/completions","/v1/messages","ws:/responses","/embeddings","/chat/completions"]},
			  {"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			  {"id":"realtime-ish","name":"RT","model_picker_enabled":true,"capabilities":{"type":"chat"},
			   "supported_endpoints":["ws:/realtime"]}]}`))
			return
		}
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	p, ok := find(All(), "copilot")
	if !ok {
		t.Fatal("no copilot provider")
	}
	if p.Base(Chat) == "" || p.Base(Responses) == "" || p.Base(Anthropic) == "" {
		t.Fatalf("copilot must declare chat, responses and anthropic: %+v", p.Speaks())
	}
	if got := fmt.Sprint(p.Speaks()); got != "[chat responses anthropic]" {
		t.Fatalf("speaks: %s", got)
	}

	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = find(All(), "copilot")
	cases := map[string]string{
		"gpt-6-sol":       "[responses]",
		"claude-sonnet-5": "[chat anthropic]",
		"gpt-4.1":         "[]",
		"realtime-ish":    "[]",
	}
	for model, want := range cases {
		got := fmt.Sprint(p.APIs(model))
		if want == "[]" {
			if p.APIs(model) != nil {
				t.Fatalf("%s: want unknown, got %s", model, got)
			}
			continue
		}
		if got != want {
			t.Fatalf("%s: got %s want %s", model, got, want)
		}
	}
	var found bool
	for _, m := range p.Available() {
		found = found || m.ID == "gpt-6-sol"
	}
	if !found {
		t.Fatalf("available models lost gpt-6-sol: %+v", p.Available())
	}
}

func TestCopilotAPIsMapping(t *testing.T) {
	if got := copilotAPIs(nil); got != nil {
		t.Fatalf("missing field should be unknown: %v", got)
	}
	if got := copilotAPIs([]string{"ws:/responses", "/embeddings"}); got != nil {
		t.Fatalf("only ignored endpoints should be unknown: %v", got)
	}
	if got := fmt.Sprint(copilotAPIs([]string{"/v1/messages", "/responses", "/responses"})); got != "[anthropic responses]" {
		t.Fatalf("mapping: %s", got)
	}
}
