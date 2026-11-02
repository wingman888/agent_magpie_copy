package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestEndpointAPIs(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"ws:/responses", "/embeddings"}, nil},
		{[]string{"/responses"}, []string{"responses"}},
		{[]string{"/v1/messages", "/chat/completions"}, []string{"chat", "anthropic"}},
		{[]string{"/responses", "ws:/responses", "/chat/completions", "/v1/messages"}, []string{"chat", "responses", "anthropic"}},
	}
	for _, c := range cases {
		if got := catalog.EndpointAPIs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("%v: got %v want %v", c.in, got, c.want)
		}
	}
}

// The APIs a list declares are saved with it and read back by Provider.APIs.
func TestProviderAPIs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	p := Provider{ID: "relay"}
	if p.APIs("x") != nil {
		t.Fatal("no list: unknown")
	}
	catalog.SaveLive("relay", "http://x", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"anthropic", "chat"}},
		{ID: "plain"},
	})
	if got := p.APIs("gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Errorf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x: %v", got)
	}
	if p.APIs("plain") != nil || p.APIs("missing") != nil {
		t.Error("undeclared is unknown")
	}
	live, _, _ := catalog.Live("relay")
	if !slices.Equal(live[0].APIs, []string{"responses"}) {
		t.Errorf("not kept in the cache: %+v", live)
	}
}

// A model list that declares endpoints is read by the generic fetch too.
func TestFetchReadsSupportedEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"gpt-x","supported_endpoints":["/responses"]},{"id":"m"}]}`))
	}))
	defer srv.Close()
	ms, err := catalog.Fetch(context.Background(), srv.URL+"/v1", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || !slices.Equal(ms[0].APIs, []string{"responses"}) || ms[1].APIs != nil {
		t.Fatalf("%+v", ms)
	}
}

func TestCopilotInitiatorAndVision(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		user, vision bool
	}{
		{"chat user", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"text","text":"see"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, true, true},
		{"anthropic tool_result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`, false, false},
		{"anthropic tool_result+text", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"},{"type":"text","text":"now this"}]}]}`, true, false},
		{"anthropic text", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true, false},
		{"anthropic image", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`, true, true},
		{"anthropic image in tool_result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type": "image","source":{}}]}]}]}`, false, true},
		{"responses string", `{"input":"hi"}`, true, false},
		{"responses user", `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true, false},
		{"responses user message", `{"input":[{"type":"message","role":"user","content":"hi"}]}`, true, false},
		{"responses output", `{"input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"x"}]}`, false, false},
		{"responses image", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`, true, true},
		{"empty", `{}`, false, false},
	}
	for _, c := range cases {
		if got := userTurn([]byte(c.body)); got != c.user {
			t.Errorf("%s: user %v", c.name, got)
		}
		if got := hasImage([]byte(c.body)); got != c.vision {
			t.Errorf("%s: vision %v", c.name, got)
		}
	}
}

// Copilot serves all three endpoints, reads which model is on which from its
// list, signs each with the right initiator, and its self-test asks each
// endpoint for a model served there.
func TestCopilotEndpoints(t *testing.T) {
	signIn(t)
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	seen := map[string]string{} // path → model, initiator, vision
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-6-sol","name":"GPT-6","model_picker_enabled":true,"supported_endpoints":["/responses","ws:/responses"],"capabilities":{"type":"chat"}},
			  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"supported_endpoints":["/v1/messages","/chat/completions"],"capabilities":{"type":"chat"}},
			  {"id":"old","name":"Old","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
			return
		}
		var in struct{ Model string }
		json.NewDecoder(r.Body).Decode(&in)
		<-mu
		seen[r.URL.Path] = in.Model + " " + r.Header.Get("X-Initiator")
		mu <- struct{}{}
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	p, _ := find(All(), "copilot")
	if p.Chat == "" || p.Responses == "" || p.Anthropic == "" || len(p.Speaks()) != 3 {
		t.Fatalf("copilot endpoints: %+v", p)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-6-sol"); !slices.Equal(got, []Protocol{Responses}) {
		t.Errorf("gpt-6-sol: %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x: %v", got)
	}
	if p.APIs("old") != nil {
		t.Error("old: unknown")
	}

	for _, r := range p.Test(context.Background()) {
		if !r.OK {
			t.Errorf("%s: %+v", r.Protocol, r)
		}
	}
	if got := seen["/chat/completions"]; !strings.HasPrefix(got, "claude-x ") {
		t.Errorf("chat probe: %q", got)
	}
	if got := seen["/responses"]; got != "gpt-6-sol user" {
		t.Errorf("responses probe: %q", got)
	}
	if got := seen["/v1/messages"]; got != "claude-x user" {
		t.Errorf("anthropic probe: %q", got)
	}

	body := []byte(`{"model":"claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`)
	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	if err := p.Sign(context.Background(), req, Anthropic, body); err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/v1/messages" || req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Copilot-Vision-Request") != "" {
		t.Fatalf("signed: %s %v", req.URL, req.Header)
	}
	body = []byte(`{"model":"gpt-6-sol","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`)
	req, _ = http.NewRequest("POST", p.Responses+"/responses", nil)
	p.Sign(context.Background(), req, Responses, body)
	if req.Header.Get("X-Initiator") != "user" || req.Header.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("signed: %v", req.Header)
	}
}

// A model the list says is served elsewhere is never the probe's pick.
func TestTestModelSkipsModelsServedElsewhere(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.SaveLive("relay", "http://x", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "plain"},
		{ID: "claude-x", APIs: []string{"chat"}},
	})
	p := Provider{ID: "relay", Chat: "http://x", Responses: "http://x", Anthropic: "http://x", Key: "k"}
	if got := p.testModel(p, Chat); got != "claude-x" {
		t.Errorf("chat: %q", got)
	}
	if got := p.testModel(p, Responses); got != "gpt-x" {
		t.Errorf("responses: %q", got)
	}
	if got := p.testModel(p, Anthropic); got != "plain" {
		t.Errorf("anthropic: %q", got)
	}
}
