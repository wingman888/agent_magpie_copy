package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestCopilotDeclaresEndpointsAndModelAPIs(t *testing.T) {
	signIn(t)
	var saw []http.Header
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-6-sol","name":"GPT-6","model_picker_enabled":true,"supported_endpoints":["/responses","ws:/responses"],"capabilities":{"type":"chat"}},
			  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"supported_endpoints":["/chat/completions","/v1/messages"],"capabilities":{"type":"chat"}},
			  {"id":"old-x","name":"Old","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			  {"id":"emb-only","name":"Odd","model_picker_enabled":true,"supported_endpoints":["/embeddings"],"capabilities":{"type":"chat"}}]}`))
			return
		}
		saw = append(saw, r.Header.Clone())
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

	p, ok := find(All(), "copilot")
	if !ok {
		t.Fatal("no copilot provider")
	}
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("copilot speaks %v", got)
	}
	if p.APIs("gpt-6-sol") != nil {
		t.Fatal("APIs known before any model list")
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-6-sol"); !slices.Equal(got, []Protocol{Responses}) {
		t.Errorf("gpt-6-sol APIs %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x APIs %v", got)
	}
	if got := p.APIs("old-x"); got != nil {
		t.Errorf("old-x APIs %v, want unknown", got)
	}
	if got := p.APIs("emb-only"); got != nil {
		t.Errorf("emb-only APIs %v, want unknown", got)
	}
	if got := p.APIs("nope"); got != nil {
		t.Errorf("unlisted model APIs %v", got)
	}
	// the cached list keeps them
	live, _, _ := catalog.Live("copilot")
	if len(live) == 0 || !slices.Equal(live[0].APIs, []string{"responses"}) {
		t.Errorf("cached list %+v", live)
	}

	// each endpoint is tested with a model served there
	if m := p.testModel(p, Responses); m != "gpt-6-sol" {
		t.Errorf("responses test model %q", m)
	}
	if m := p.testModel(p, Chat); m != "claude-x" {
		t.Errorf("chat test model %q", m)
	}
	if m := p.testModel(p, Anthropic); m != "claude-x" {
		t.Errorf("anthropic test model %q", m)
	}
}

func TestCatalogEndpointAPIs(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want []string
	}{
		{nil, nil},
		{[]string{"/chat/completions", "/responses", "/v1/messages"}, []string{"chat", "responses", "anthropic"}},
		{[]string{"ws:/responses", "/embeddings"}, nil},
		{[]string{"/responses", "/responses"}, []string{"responses"}},
	} {
		if got := catalog.EndpointAPIs(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("EndpointAPIs(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestProviderAPIsFromLiveList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("relay", "http://x", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "bogus", "chat"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "relay"}
	if got := p.APIs("gpt-x"); !slices.Equal(got, []Protocol{Responses}) {
		t.Errorf("gpt-x %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Chat}) {
		t.Errorf("claude-x %v", got)
	}
	if p.APIs("plain") != nil || p.APIs("missing") != nil || (Provider{ID: "other"}).APIs("gpt-x") != nil {
		t.Error("unknown models should have nil APIs")
	}
}

func TestCopilotInitiatorAndVision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		user   bool
		vision bool
	}{
		{"chat user", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, true, true},
		{"anthropic tool results only", `{"messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"n","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`, false, false},
		{"anthropic tool result and text", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"},{"type":"text","text":"now this"}]}]}`, true, false},
		{"anthropic assistant last", `{"messages":[{"role":"user","content":"go"},{"role":"assistant","content":"ok"}]}`, false, false},
		{"anthropic image", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}},{"type":"text","text":"what"}]}]}`, true, true},
		{"anthropic image in tool result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","data":"AA"}}]}]}]}`, false, true},
		{"responses string", `{"input":"hi"}`, true, false},
		{"responses user item", `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true, false},
		{"responses user message item", `{"input":[{"type":"message","role":"user","content":"hi"}]}`, true, false},
		{"responses function output", `{"input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"c","name":"n","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"x"}]}`, false, false},
		{"responses image", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`, true, true},
		{"text mentioning image", `{"messages":[{"role":"user","content":"type \"image\" please"}]}`, true, false},
	} {
		if got := userTurn([]byte(tc.body)); got != tc.user {
			t.Errorf("%s: userTurn = %v, want %v", tc.name, got, tc.user)
		}
		if got := hasImage([]byte(tc.body)); got != tc.vision {
			t.Errorf("%s: hasImage = %v, want %v", tc.name, got, tc.vision)
		}
	}
}

func TestCopilotSignSetsInitiatorAndVision(t *testing.T) {
	signIn(t)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix()})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}
	p, _ := find(All(), "copilot")

	sign := func(proto Protocol, body string) http.Header {
		t.Helper()
		req, _ := http.NewRequest("POST", p.Base(proto)+"/x", strings.NewReader(body))
		if err := p.Sign(context.Background(), req, proto, []byte(body)); err != nil {
			t.Fatal(err)
		}
		return req.Header
	}
	h := sign(Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}]}`)
	if h.Get("X-Initiator") == "user" {
		t.Errorf("tool results only: %v", h)
	}
	h = sign(Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"},{"type":"text","text":"and"}]}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Errorf("tool result with text: %v", h)
	}
	h = sign(Responses, `{"input":"hello"}`)
	if h.Get("X-Initiator") != "user" {
		t.Errorf("responses string input: %v", h)
	}
	h = sign(Anthropic, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AA"}}]}]}`)
	if h.Get("Copilot-Vision-Request") != "true" {
		t.Errorf("anthropic image: %v", h)
	}
	h = sign(Responses, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`)
	if h.Get("Copilot-Vision-Request") != "true" {
		t.Errorf("responses image: %v", h)
	}
}
