package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// copilotListServer serves a Copilot /models answer and points a cached
// session at it, so copilotModels never reaches GitHub.
func copilotListServer(t *testing.T, body string) copilotApp {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	const gh = "gho_endpoints_test"
	s := copilotSession{Token: "session", ExpiresAt: time.Now().Add(time.Hour).Unix()}
	s.Endpoints.API = srv.URL
	copilotMu.Lock()
	copilotSessions[gh] = s
	copilotMu.Unlock()
	t.Cleanup(func() {
		copilotMu.Lock()
		delete(copilotSessions, gh)
		copilotMu.Unlock()
	})
	return copilotApp{User: "octocat", Token: gh}
}

const copilotEndpointsList = `{"data":[
  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,
   "capabilities":{"type":"chat"},
   "supported_endpoints":["/responses","/websocket"]},
  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,
   "capabilities":{"type":"chat"},
   "supported_endpoints":["/chat/completions","/v1/messages","/embeddings"]},
  {"id":"old-model","name":"Old","model_picker_enabled":true,
   "capabilities":{"type":"chat"}}
]}`

func TestCopilotModelsParseSupportedEndpoints(t *testing.T) {
	app := copilotListServer(t, copilotEndpointsList)
	ms, err := copilotModels(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]catalog.Model{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	if got := byID["gpt-6-sol"].APIs; !slices.Equal(got, []string{"responses"}) {
		t.Fatalf("gpt-6-sol APIs: %v", got)
	}
	if got := byID["claude-x"].APIs; !slices.Equal(got, []string{"chat", "anthropic"}) {
		t.Fatalf("claude-x APIs: %v", got)
	}
	if got, ok := byID["old-model"]; !ok || got.APIs != nil {
		t.Fatalf("a model without supported_endpoints must be unknown: %+v", got)
	}
}

func TestCopilotAPIsMapping(t *testing.T) {
	if got := copilotAPIs([]string{"/responses", "/responses", "/chat/completions", "/v1/messages"}); !slices.Equal(got, []string{"responses", "chat", "anthropic"}) {
		t.Fatalf("mapping: %v", got)
	}
	if got := copilotAPIs([]string{"/websocket", "/embeddings"}); got != nil {
		t.Fatalf("only unknown paths must be unknown, got %v", got)
	}
	if got := copilotAPIs(nil); got != nil {
		t.Fatalf("missing list must be unknown, got %v", got)
	}
}

func TestProviderAPIsFromLastList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("copilot", "https://x", []catalog.Model{
		{ID: "gpt-6-sol", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"anthropic", "chat"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "copilot"}
	if got := p.APIs("gpt-6-sol"); !slices.Equal(got, []Protocol{Responses}) {
		t.Fatalf("gpt-6-sol: %v", got)
	}
	// reported in Protocols order, whatever order the list gave them
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x: %v", got)
	}
	if got := p.APIs("plain"); got != nil {
		t.Fatalf("a model with no declared API is unknown: %v", got)
	}
	if got := p.APIs("not-in-list"); got != nil {
		t.Fatalf("a model not in the list is unknown: %v", got)
	}
	// another provider's list is not this one's
	if got := (Provider{ID: "other"}).APIs("gpt-6-sol"); got != nil {
		t.Fatalf("no list for provider: %v", got)
	}
}

func TestProviderAPIsIgnoresUnknownNames(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("p", "https://x", []catalog.Model{
		{ID: "m", APIs: []string{"gemini", "responses"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := (Provider{ID: "p"}).APIs("m"); !slices.Equal(got, []Protocol{Responses}) {
		t.Fatalf("unknown names must be dropped: %v", got)
	}
}

func TestCopilotProviderSpeaksThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "octocat", Token: "gho_x"}, "")
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("copilot speaks: %v", got)
	}
	for _, pr := range []Protocol{Chat, Responses, Anthropic} {
		if p.Base(pr) != copilotBase {
			t.Fatalf("%s base: %q", pr, p.Base(pr))
		}
	}
}
