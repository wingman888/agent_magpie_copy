package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// Provider.APIs reads the declared APIs from the last saved model list.
func TestS1ModelAPIsFromLiveList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "s1-apis-live"}
	if got := p.APIs("gpt-x"); got != nil {
		t.Fatalf("no list yet: got %v", got)
	}
	err := catalog.SaveLive(p.ID, "https://example.test/v1", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses", "bogus"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic"}},
		{ID: "plain"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Errorf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x: %v", got)
	}
	if got := p.APIs("plain"); got != nil {
		t.Errorf("undeclared: %v", got)
	}
	if got := p.APIs("missing"); got != nil {
		t.Errorf("unknown model: %v", got)
	}
}

// The APIs field survives the cache round trip, and an old cache without it
// reads back as unknown.
func TestS1ModelAPIsCacheFormat(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("s1-apis-cache", "", []catalog.Model{{ID: "m", APIs: []string{"chat"}}}); err != nil {
		t.Fatal(err)
	}
	ms, _, ok := catalog.Live("s1-apis-cache")
	if !ok || len(ms) != 1 || !slices.Equal(ms[0].APIs, []string{"chat"}) {
		t.Fatalf("round trip: %+v %v", ms, ok)
	}
	if err := catalog.SaveLive("s1-apis-old", "", []catalog.Model{{ID: "m"}}); err != nil {
		t.Fatal(err)
	}
	ms, _, ok = catalog.Live("s1-apis-old")
	if !ok || len(ms) != 1 || ms[0].APIs != nil {
		t.Fatalf("unknown should be nil: %+v %v", ms, ok)
	}
}

// Copilot's supported_endpoints become APIs; other endpoints are ignored and
// a missing field stays unknown.
func TestS1CopilotSupportedEndpoints(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s1-sess" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"data":[
		  {"id":"gpt-s1","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/responses","/chat/completions","ws:/responses"]},
		  {"id":"claude-s1","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/v1/messages","/chat/completions","/embeddings"]},
		  {"id":"legacy-s1","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "s1-sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	oldURL := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = oldURL }()
	copilotSessions = map[string]copilotSession{}

	ms, err := copilotModels(context.Background(), copilotApp{User: "s1", Token: "gho_s1"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"gpt-s1":    {"responses", "chat"},
		"claude-s1": {"anthropic", "chat"},
		"legacy-s1": nil,
	}
	if len(ms) != len(want) {
		t.Fatalf("models: %+v", ms)
	}
	for _, m := range ms {
		if !slices.Equal(m.APIs, want[m.ID]) {
			t.Errorf("%s APIs %v, want %v", m.ID, m.APIs, want[m.ID])
		}
	}
}

// Copilot declares all three endpoints.
func TestS1CopilotDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "s1"}, "Pro")
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks %v", got)
	}
}

// The self-test asks each endpoint only a model the list says it serves there.
func TestS1SelfTestPicksDeclaredEndpoint(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var paths []string
	var models = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			rw.WriteHeader(404) // keep the list saved below
			return
		}
		var in struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		paths = append(paths, r.URL.Path)
		models[r.URL.Path] = in.Model
		_, _ = rw.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := Provider{ID: "s1-selftest", Name: "S1", Key: "sk-s1", Chat: srv.URL + "/v1", Anthropic: srv.URL}
	if err := catalog.SaveLive(p.ID, p.Chat, []catalog.Model{
		{ID: "gpt-s1", APIs: []string{"responses"}},
		{ID: "claude-s1", APIs: []string{"chat"}},
	}); err != nil {
		t.Fatal(err)
	}
	res := p.Test(context.Background())
	if len(res) != 2 {
		t.Fatalf("results %+v", res)
	}
	if !res[0].OK || res[0].Model != "claude-s1" || models["/v1/chat/completions"] != "claude-s1" {
		t.Errorf("chat probe: %+v (sent %v)", res[0], models)
	}
	if res[1].OK || res[1].Model != "" {
		t.Errorf("anthropic probe should have no model: %+v", res[1])
	}
	if !slices.Equal(paths, []string{"/v1/chat/completions"}) {
		t.Errorf("requests sent: %v", paths)
	}
}
