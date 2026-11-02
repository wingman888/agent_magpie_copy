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

func TestCopilotAPIs(t *testing.T) {
	got := copilotAPIs([]string{"/chat/completions", "ws:/responses", "/v1/messages", "/embeddings", "/responses", "/responses"})
	if want := []string{"chat", "anthropic", "responses"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
	if copilotAPIs(nil) != nil || copilotAPIs([]string{"/embeddings"}) != nil {
		t.Error("unknown should be nil")
	}
}

func TestProviderAPIs(t *testing.T) {
	isolate(t)
	p := Provider{ID: "relay", Chat: "http://x"}
	if p.APIs("gpt-x") != nil {
		t.Error("no list should be unknown")
	}
	catalog.SaveLive("relay", "http://x", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses", "bogus"}}, {ID: "claude-x", APIs: []string{"chat", "anthropic"}}, {ID: "free"}})
	if got := p.APIs("gpt-x"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Errorf("gpt-x %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x %v", got)
	}
	if p.APIs("free") != nil || p.APIs("nope") != nil {
		t.Error("undeclared should be nil")
	}
}

func TestCopilotModelsReadEndpoints(t *testing.T) {
	isolate(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[
		 {"id":"gpt-6","name":"GPT 6","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses","ws:/responses"]},
		 {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages"]},
		 {"id":"old","name":"Old","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	defer api.Close()
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(time.Hour).Unix(),
			"endpoints": map[string]string{"api": api.URL}})
	}))
	defer tok.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tok.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	p := copilotProvider(copilotApp{User: "u", Token: "gho_x"}, "")
	if got := p.Speaks(); !reflect.DeepEqual(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks %v", got)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-6"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Errorf("gpt-6 %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Errorf("claude-x %v", got)
	}
	if p.APIs("old") != nil {
		t.Error("missing list should be unknown")
	}
}

func TestCopilotTurn(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		user, vision bool
	}{
		{"chat user", `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool", `{"messages":[{"role":"user","content":"hi"},{"role":"assistant"},{"role":"tool","content":"r"}]}`, false, false},
		{"chat image", `{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"data:x"}}]}]}`, true, true},
		{"anthropic tool_result only", `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`, false, false},
		{"anthropic tool_result+text", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"},{"type":"text","text":"more"}]}]}`, true, false},
		{"anthropic string", `{"messages":[{"role":"user","content":"hello"}]}`, true, false},
		{"anthropic image", `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"x"}}]}]}`, true, true},
		{"anthropic assistant last", `{"messages":[{"role":"assistant","content":"x"}]}`, false, false},
		{"responses string", `{"input":"hi"}`, true, false},
		{"responses user", `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"a"}]}]}`, true, false},
		{"responses role only", `{"input":[{"role":"user","content":"a"}]}`, true, false},
		{"responses output", `{"input":[{"role":"user","content":"a"},{"type":"function_call","call_id":"c"},{"type":"function_call_output","call_id":"c","output":"o"}]}`, false, false},
		{"responses image", `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:x"}]}]}`, true, true},
		{"bad", `nope`, false, false},
	}
	for _, c := range cases {
		u, v := copilotTurn([]byte(c.body))
		if u != c.user || v != c.vision {
			t.Errorf("%s: got user=%v vision=%v", c.name, u, v)
		}
	}
}

func TestCopilotSignHeaders(t *testing.T) {
	isolate(t)
	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "t", "expires_at": time.Now().Add(time.Hour).Unix()})
	}))
	defer tok.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tok.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}
	p := copilotProvider(copilotApp{User: "u", Token: "gho_x"}, "")
	body := `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"d"}]}]}`
	req, _ := http.NewRequest("POST", "http://x/responses", nil)
	if err := p.Sign(context.Background(), req, Responses, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Initiator") != "user" || req.Header.Get("Copilot-Vision-Request") != "true" {
		t.Errorf("headers %v", req.Header)
	}
	req, _ = http.NewRequest("POST", "http://x/v1/messages", nil)
	p.Sign(context.Background(), req, Anthropic, []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"x"}]}]}`))
	if req.Header.Get("X-Initiator") != "agent" || req.Header.Get("Copilot-Vision-Request") != "" {
		t.Errorf("headers %v", req.Header)
	}
}

func TestTestPicksModelServedOnEndpoint(t *testing.T) {
	isolate(t)
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = rw.Write([]byte(`{"data":[{"id":"gpt-x"},{"id":"claude-x"}]}`))
			return
		}
		var in struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		seen[r.URL.Path] = in.Model
		_, _ = rw.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := Provider{ID: "relay2", Name: "Relay", Chat: srv.URL + "/v1", Responses: srv.URL + "/v1", Anthropic: srv.URL, Key: "k"}
	catalog.SaveLive("relay2", srv.URL, []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses"}}, {ID: "claude-x", APIs: []string{"chat", "anthropic"}}})
	for _, r := range p.Test(context.Background()) {
		if !r.OK {
			t.Errorf("%+v", r)
		}
	}
	want := map[string]string{"/v1/chat/completions": "claude-x", "/v1/responses": "gpt-x", "/v1/messages": "claude-x"}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("got %v", seen)
	}
}
