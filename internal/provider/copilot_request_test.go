package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestUserTurn(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"chat user last":            {`{"messages":[{"role":"user","content":"hi"}]}`, true},
		"chat tool last":            {`{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false},
		"chat user text parts":      {`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true},
		"anthropic only tool_result": {`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}]}`, false},
		"anthropic tool_result+text": {`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"},{"type":"text","text":"more"}]}]}`, true},
		"anthropic string content":   {`{"messages":[{"role":"user","content":"hello"}]}`, true},
		"responses string input":     {`{"input":"hello"}`, true},
		"responses function output":  {`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]},{"type":"function_call_output","call_id":"c","output":"o"}]}`, false},
		"responses user message":     {`{"input":[{"type":"function_call_output","call_id":"c","output":"o"},{"type":"message","role":"user","content":"next"}]}`, true},
		"responses null input":       {`{"input":null,"messages":[{"role":"user","content":"hi"}]}`, true},
		"garbage":                    {`not json`, false},
	}
	for name, c := range cases {
		if got := userTurn([]byte(c.body)); got != c.want {
			t.Errorf("%s: userTurn = %v, want %v", name, got, c.want)
		}
	}
}

func TestHasImage(t *testing.T) {
	cases := map[string]struct {
		body string
		want bool
	}{
		"chat image_url":     {`{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, true},
		"responses input":    {`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`, true},
		"anthropic image":    {`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`, true},
		"anthropic in tool":  {`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":[{"type":"image","source":{}}]}]}]}`, true},
		"text only":          {`{"messages":[{"role":"user","content":"a picture of image_url"}]}`, false},
		"image only in tools": {`{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"image"}]}`, false},
	}
	for name, c := range cases {
		if got := hasImage([]byte(c.body)); got != c.want {
			t.Errorf("%s: hasImage = %v, want %v", name, got, c.want)
		}
	}
}

func TestCopilotAPIs(t *testing.T) {
	got := copilotAPIs([]string{"/responses", "websocket", "/chat/completions", "/responses", "/v1/messages", "/embeddings"})
	if want := []string{"responses", "chat", "anthropic"}; !slices.Equal(got, want) {
		t.Fatalf("copilotAPIs = %v, want %v", got, want)
	}
	if got := copilotAPIs(nil); got != nil {
		t.Fatalf("missing endpoints should be unknown, got %v", got)
	}
	if got := copilotAPIs([]string{"/embeddings"}); got != nil {
		t.Fatalf("no speakable endpoint should be unknown, got %v", got)
	}
}

func TestCopilotDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "octocat"}, "Pro")
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks: %v", got)
	}
	for _, pr := range []Protocol{Chat, Responses, Anthropic} {
		if p.Base(pr) != copilotBase {
			t.Fatalf("base for %s: %q", pr, p.Base(pr))
		}
	}
}

func TestCopilotModelsReadSupportedEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"data":[
		  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses","/chat/completions"]},
		  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages"]},
		  {"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	defer srv.Close()

	const token = "gho_endpoints_test"
	old := copilotSessions
	copilotSessions = map[string]copilotSession{token: {Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix(), Endpoints: struct {
		API string `json:"api"`
	}{API: srv.URL}}}
	defer func() { copilotSessions = old }()

	ms, err := copilotModels(context.Background(), copilotApp{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]catalog.Model{}
	for _, m := range ms {
		byID[m.ID] = m
	}
	if got := byID["gpt-6-sol"].APIs; !slices.Equal(got, []string{"responses", "chat"}) {
		t.Fatalf("gpt-6-sol APIs: %v", got)
	}
	if got := byID["claude-x"].APIs; !slices.Equal(got, []string{"chat", "anthropic"}) {
		t.Fatalf("claude-x APIs: %v", got)
	}
	if got := byID["gpt-4.1"].APIs; got != nil {
		t.Fatalf("gpt-4.1 should be unknown, got %v", got)
	}
}

func TestProviderAPIsFromLiveList(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	p := Provider{ID: "vendor", Name: "Vendor", Chat: "https://example.test/v1"}
	if got := p.APIs("gpt-x"); got != nil {
		t.Fatalf("no list yet: %v", got)
	}
	if err := catalog.SaveLive("vendor", p.Chat, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic", "gemini"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x: %v", got)
	}
	if got := p.APIs("plain"); got != nil {
		t.Fatalf("plain should be unknown: %v", got)
	}
	if got := p.APIs("missing"); got != nil {
		t.Fatalf("missing model should be unknown: %v", got)
	}

	// an old cache with no APIs field still reads, and says nothing
	old, _ := json.Marshal(map[string]any{"fetched": time.Now(), "base": "x", "models": []map[string]string{{"ID": "gpt-x"}}})
	if err := os.MkdirAll(filepath.Dir(catalog.LivePath("vendor")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.LivePath("vendor"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-x"); got != nil {
		t.Fatalf("old cache should be unknown: %v", got)
	}
}
