package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// copilotStub is a GitHub-and-Copilot stand-in: the token exchange answers
// with its own base URL as the API endpoint, and /models serves the given
// body.
func copilotStub(t *testing.T, models string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/copilot_token":
			json.NewEncoder(w).Encode(map[string]any{
				"token":      "sess",
				"expires_at": time.Now().Add(time.Hour).Unix(),
				"endpoints":  map[string]string{"api": "http://" + r.Host},
			})
		case "/models":
			w.Write([]byte(models))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)

	oldTok := CopilotTokenURL
	CopilotTokenURL = srv.URL + "/copilot_token"
	copilotMu.Lock()
	copilotSessions = map[string]copilotSession{}
	copilotMu.Unlock()
	t.Cleanup(func() {
		CopilotTokenURL = oldTok
		copilotMu.Lock()
		copilotSessions = map[string]copilotSession{}
		copilotMu.Unlock()
	})
}

const copilotModelsBody = `{"data":[
  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,
   "capabilities":{"type":"chat"},
   "supported_endpoints":["/responses","ws:/responses"]},
  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,
   "capabilities":{"type":"chat"},
   "supported_endpoints":["/chat/completions","/v1/messages","/embeddings"]},
  {"id":"mixed","name":"Mixed","model_picker_enabled":true,
   "capabilities":{"type":"chat"},
   "supported_endpoints":["/responses","/chat/completions","/responses"]},
  {"id":"silent","name":"Silent","model_picker_enabled":true,
   "capabilities":{"type":"chat"}},
  {"id":"odd","name":"Odd","model_picker_enabled":true,
   "capabilities":{"type":"chat"},
   "supported_endpoints":["ws:/responses","/embeddings"]}
]}`

func TestCopilotDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "octocat", Token: "gho_x"}, "Pro")
	if got, want := p.Speaks(), []Protocol{Chat, Responses, Anthropic}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Speaks() = %v, want %v", got, want)
	}
	for _, pr := range []Protocol{Chat, Responses, Anthropic} {
		if p.Base(pr) != copilotBase {
			t.Errorf("Base(%s) = %q, want %q", pr, p.Base(pr), copilotBase)
		}
	}
}

func TestCopilotModelsReadSupportedEndpoints(t *testing.T) {
	copilotStub(t, copilotModelsBody)
	p := copilotProvider(copilotApp{User: "octocat", Token: "gho_x"}, "Pro")

	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string][]string{}
	for _, m := range ms {
		byID[m.ID] = m.APIs
	}
	if len(byID) != 5 {
		t.Fatalf("models: %+v", ms)
	}
	want := map[string][]string{
		"gpt-6-sol": {"responses"},
		"claude-x":  {"chat", "anthropic"},
		"mixed":     {"responses", "chat"},
		"silent":    nil,
		"odd":       nil,
	}
	for id, w := range want {
		if !reflect.DeepEqual(byID[id], w) {
			t.Errorf("%s: APIs = %v, want %v", id, byID[id], w)
		}
	}
}

func TestProviderAPIsFromCachedList(t *testing.T) {
	copilotStub(t, copilotModelsBody)
	p := copilotProvider(copilotApp{User: "octocat", Token: "gho_x"}, "Pro")
	// The list is cached by the fetch; APIs reads it back from the cache.
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := catalog.Live("copilot"); !ok {
		t.Fatal("fetch did not cache the list")
	}

	cases := map[string][]Protocol{
		"gpt-6-sol": {Responses},
		"claude-x":  {Chat, Anthropic},
		"mixed":     {Responses, Chat},
		"silent":    nil,
		"odd":       nil,
		"unknown":   nil,
	}
	for model, want := range cases {
		if got := p.APIs(model); !reflect.DeepEqual(got, want) {
			t.Errorf("APIs(%q) = %v, want %v", model, got, want)
		}
	}
}

func TestProviderAPIsNoList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if got := (Provider{ID: "nolist"}).APIs("anything"); got != nil {
		t.Fatalf("APIs with no list = %v, want nil", got)
	}
}

func TestProviderAPIsDropsUnknownValues(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("weird", "http://x", []catalog.Model{
		{ID: "m", APIs: []string{"websocket", "anthropic", "anthropic", "gemini"}},
		{ID: "only-junk", APIs: []string{"websocket"}},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "weird"}
	if got, want := p.APIs("m"), []Protocol{Anthropic}; !reflect.DeepEqual(got, want) {
		t.Errorf("APIs(m) = %v, want %v", got, want)
	}
	if got := p.APIs("only-junk"); got != nil {
		t.Errorf("APIs(only-junk) = %v, want nil", got)
	}
}
