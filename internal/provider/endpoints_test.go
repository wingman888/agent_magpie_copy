package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

func TestCopilotTurn(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		user, vision bool
	}{
		{"chat user", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat assistant last", `{"messages":[{"role":"assistant","content":"x"}]}`, false, false},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, true, true},
		{"anthropic user", `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true, false},
		{"anthropic tool_result only", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`, false, false},
		{"anthropic tool_result and text", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"},{"type":"text","text":"now this"}]}]}`, true, false},
		{"anthropic image", `{"messages":[{"role":"user","content":[{"type": "image","source":{"type":"base64","data":"x"}},{"type":"text","text":"?"}]}]}`, true, true},
		{"anthropic image in tool_result", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":[{"type":"image","source":{}}]}]}]}`, false, true},
		{"responses string", `{"input":"hi"}`, true, false},
		{"responses user message", `{"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true, false},
		{"responses typed message", `{"input":[{"type":"message","role":"user","content":"hi"}]}`, true, false},
		{"responses function output", `{"input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"1","name":"x","arguments":"{}"},{"type":"function_call_output","call_id":"1","output":"ok"}]}`, false, false},
		{"responses image", `{"input":[{"role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"data:x"}]}]}`, true, true},
		{"image word only in text", `{"messages":[{"role":"user","content":"an image of \"image_url\""}]}`, true, false},
		{"not json", `x`, false, false},
	} {
		user, vision := copilotTurn([]byte(tc.body))
		if user != tc.user || vision != tc.vision {
			t.Errorf("%s: user=%v vision=%v, want %v %v", tc.name, user, vision, tc.user, tc.vision)
		}
	}
}

func TestCopilotAPIs(t *testing.T) {
	got := copilotAPIs([]string{"/chat/completions", "ws:/responses", "/responses", "/v1/messages", "/embeddings", "/chat/completions"})
	if !reflect.DeepEqual(got, []string{"chat", "responses", "anthropic"}) {
		t.Fatalf("%v", got)
	}
	if copilotAPIs(nil) != nil || copilotAPIs([]string{"/embeddings"}) != nil {
		t.Fatal("unknown must be nil")
	}
}

func TestProviderAPIsFromLiveList(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "gw", Name: "GW", Key: "k", Chat: "https://gw.example/v1"}
	if p.APIs("a") != nil {
		t.Fatal("no list, no apis")
	}
	if err := catalog.SaveLive("gw", "https://gw.example", []catalog.Model{
		{ID: "a", APIs: []string{"responses"}}, {ID: "b"}, {ID: "c", APIs: []string{"chat", "bogus", "anthropic"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("a"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Errorf("a: %v", got)
	}
	if p.APIs("b") != nil || p.APIs("nope") != nil {
		t.Error("undeclared must be nil")
	}
	if got := p.APIs("c"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("c: %v", got)
	}
}

func TestTestPicksModelServedOnEndpoint(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	p := Provider{ID: "gw", Name: "GW", Key: "k", Chat: "https://gw.example/v1", Responses: "https://gw.example/v1", Anthropic: "https://gw.example"}
	catalog.SaveLive("gw", "https://gw.example", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic"}},
		{ID: "llama", APIs: []string{"chat"}},
	})
	for proto, want := range map[Protocol]string{Chat: "claude-x", Responses: "gpt-x", Anthropic: "claude-x"} {
		if got := p.testModel(p, proto); got != want {
			t.Errorf("%s: %q, want %q", proto, got, want)
		}
	}
}

func TestCopilotDeclaresEndpointsAndHeaders(t *testing.T) {
	signIn(t)
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-6-sol","name":"G","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses","ws:/responses"]},
			  {"id":"claude-x","name":"C","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages"]},
			  {"id":"mystery","name":"M","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
			return
		}
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
	if p.Chat == "" || p.Responses == "" || p.Anthropic == "" {
		t.Fatalf("copilot endpoints: %+v", p)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = find(All(), "copilot")
	if got := p.APIs("gpt-6-sol"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Errorf("gpt-6-sol: %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x: %v", got)
	}
	if p.APIs("mystery") != nil {
		t.Error("missing supported_endpoints is unknown")
	}

	sign := func(proto Protocol, path, body string) http.Header {
		req, _ := http.NewRequest("POST", p.Base(proto)+path, nil)
		if err := p.Sign(context.Background(), req, proto, []byte(body)); err != nil {
			t.Fatal(err)
		}
		return req.Header
	}
	h := sign(Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`)
	if h.Get("X-Initiator") == "user" {
		t.Error("tool_result-only turn marked user")
	}
	h = sign(Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"},{"type":"text","text":"x"}]}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Error("mixed turn not marked user")
	}
	h = sign(Responses, "/responses", `{"input":"hi"}`)
	if h.Get("X-Initiator") != "user" {
		t.Error("string input not marked user")
	}
	h = sign(Responses, "/responses", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`)
	if h.Get("Copilot-Vision-Request") != "true" {
		t.Error("input_image without vision header")
	}
	h = sign(Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`)
	if h.Get("Copilot-Vision-Request") != "true" || !strings.HasPrefix(h.Get("Authorization"), "Bearer ") {
		t.Errorf("anthropic image: %v", h)
	}
}
