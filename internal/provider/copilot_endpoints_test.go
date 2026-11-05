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

func TestProviderAPIsFromLiveList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("apitest", "", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses", "bogus"}},
		{ID: "claude-x", APIs: []string{"chat"}},
		{ID: "plain"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "apitest"}
	if got := p.APIs("gpt-x"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat}) {
		t.Fatalf("claude-x: %v", got)
	}
	if got := p.APIs("plain"); got != nil {
		t.Fatalf("undeclared model should be nil: %v", got)
	}
	if got := p.APIs("missing"); got != nil {
		t.Fatalf("unknown model should be nil: %v", got)
	}
	if got := (Provider{ID: "nolist"}).APIs("gpt-x"); got != nil {
		t.Fatalf("no live list should be nil: %v", got)
	}
}

func TestCopilotAPIsMapping(t *testing.T) {
	got := copilotAPIs([]string{"/chat/completions", "/responses", "/v1/messages", "ws:/responses", "/embeddings"})
	if !reflect.DeepEqual(got, []string{"chat", "responses", "anthropic"}) {
		t.Fatalf("mapping: %v", got)
	}
	if copilotAPIs(nil) != nil {
		t.Fatal("missing endpoints should stay unknown")
	}
	if copilotAPIs([]string{"/embeddings"}) != nil {
		t.Fatal("only ignored endpoints should stay unknown")
	}
}

func TestCopilotUserTurn(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
		want bool
	}{
		{"chat user", "/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, true},
		{"chat tool reply", "/chat/completions", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false},
		{"anthropic text", "/v1/messages", `{"messages":[{"role":"user","content":"hi"}]}`, true},
		{"anthropic string content", "/v1/messages", `{"messages":[{"role":"user","content":"hi"}]}`, true},
		{"anthropic only tool_result", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}]}`, false},
		{"anthropic mixed tool_result and text", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"},{"type":"text","text":"go on"}]}]}`, true},
		{"anthropic assistant last", "/v1/messages", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`, false},
		{"anthropic count_tokens", "/v1/messages/count_tokens", `{"messages":[{"role":"user","content":"hi"}]}`, true},
		{"responses string input", "/responses", `{"input":"hello"}`, true},
		{"responses user message", "/responses", `{"input":[{"type":"message","role":"user","content":"hi"}]}`, true},
		{"responses function output", "/responses", `{"input":[{"type":"message","role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"x"}]}`, false},
		{"responses empty", "/responses", `{}`, false},
	}
	for _, c := range cases {
		if got := copilotUserTurn(c.path, []byte(c.body)); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestCopilotHasImage(t *testing.T) {
	yes := []string{
		`{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`,
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x"},{"type":"input_image","image_url":"data:x"}]}]}`,
		`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`,
	}
	for _, b := range yes {
		if !copilotHasImage([]byte(b)) {
			t.Errorf("expected image: %s", b)
		}
	}
	if copilotHasImage([]byte(`{"messages":[{"role":"user","content":"a text saying image_url"}]}`)) {
		t.Error("plain text is not an image")
	}
}

func TestCopilotModelsCarryAPIsAndHeaders(t *testing.T) {
	signIn(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-x","name":"GPT X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses","/ws"]},
			  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages"]},
			  {"id":"old","name":"Old","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
		}
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
		t.Fatal("copilot not listed")
	}
	if p.Chat == "" || p.Responses == "" || p.Anthropic == "" {
		t.Fatalf("copilot should declare chat, responses and anthropic: %+v", p)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = find(All(), "copilot")
	if got := p.APIs("gpt-x"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x apis: %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x apis: %v", got)
	}
	if got := p.APIs("old"); got != nil {
		t.Fatalf("old model should be unknown: %v", got)
	}

	sign := func(base, path, body string) http.Header {
		req, _ := http.NewRequest("POST", base+path, nil)
		if err := p.Sign(context.Background(), req, Chat, []byte(body)); err != nil {
			t.Fatal(err)
		}
		return req.Header
	}
	if h := sign(p.Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}]}`); h.Get("X-Initiator") != "agent" {
		t.Errorf("pure tool_result should be agent: %v", h)
	}
	if h := sign(p.Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"},{"type":"text","text":"more"}]}]}`); h.Get("X-Initiator") != "user" {
		t.Errorf("mixed tool_result and text should be user: %v", h)
	}
	if h := sign(p.Responses, "/responses", `{"input":"hello"}`); h.Get("X-Initiator") != "user" {
		t.Errorf("string input should be user: %v", h)
	}
	if h := sign(p.Responses, "/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`); h.Get("Copilot-Vision-Request") != "true" || h.Get("X-Initiator") != "user" {
		t.Errorf("responses image: %v", h)
	}
	if h := sign(p.Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`); h.Get("Copilot-Vision-Request") != "true" {
		t.Errorf("anthropic image: %v", h)
	}
	if h := sign(p.Chat, "/chat/completions", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`); h.Get("X-Initiator") != "agent" || h.Get("Copilot-Vision-Request") != "" {
		t.Errorf("chat tool reply: %v", h)
	}
}
