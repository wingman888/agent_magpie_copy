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

// s1Copilot signs in a Copilot account whose session points at api, with
// the token endpoint faked, and returns that provider.
func s1Copilot(t *testing.T, api string) Provider {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_s1" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api}})
	}))
	t.Cleanup(tokens.Close)
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	copilotSessions = map[string]copilotSession{}
	t.Cleanup(func() { copilotSessions = map[string]copilotSession{} })
	return copilotProvider(copilotApp{User: "s1", Token: "gho_s1"}, "")
}

// s1Initiator signs a request to path with body and reports the headers
// magpie sets for Copilot.
func s1Initiator(t *testing.T, path string, body string) (initiator, vision string) {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	p := s1Copilot(t, srv.URL)
	proto := copilotProtocol(path)
	req, _ := http.NewRequest("POST", copilotBase+path, nil)
	if err := p.Sign(context.Background(), req, proto, []byte(body)); err != nil {
		t.Fatal(err)
	}
	return req.Header.Get("X-Initiator"), req.Header.Get("Copilot-Vision-Request")
}

func TestS1CopilotInitiatorAndVision(t *testing.T) {
	cases := []struct {
		name, path, body, initiator, vision string
	}{
		{"anthropic tool_result only is agent", "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`,
			"agent", ""},
		{"anthropic tool_result mixed with text is user", "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"thanks"}]}]}`,
			"user", ""},
		{"anthropic string content is user", "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":"hello"}]}`,
			"user", ""},
		{"anthropic image block sets vision", "/v1/messages",
			`{"model":"claude-x","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"text","text":"what is this"}]}]}`,
			"user", "true"},
		{"chat tool message last is agent", "/chat/completions",
			`{"model":"gpt-x","messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[]},{"role":"tool","content":"x"}]}`,
			"agent", ""},
		{"chat user message is user", "/chat/completions",
			`{"model":"gpt-x","messages":[{"role":"user","content":"hi"}]}`,
			"user", ""},
		{"chat image_url sets vision", "/chat/completions",
			`{"model":"gpt-x","messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`,
			"user", "true"},
		{"responses string input is user", "/responses",
			`{"model":"gpt-x","input":"hello"}`,
			"user", ""},
		{"responses function output is agent", "/responses",
			`{"model":"gpt-x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"function_call","call_id":"c1","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`,
			"agent", ""},
		{"responses user message is user with input_image vision", "/responses",
			`{"model":"gpt-x","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`,
			"user", "true"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ini, vis := s1Initiator(t, c.path, c.body)
			if ini != c.initiator {
				t.Errorf("X-Initiator = %q, want %q", ini, c.initiator)
			}
			if vis != c.vision {
				t.Errorf("Copilot-Vision-Request = %q, want %q", vis, c.vision)
			}
		})
	}
}

func TestS1CopilotDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "s1", Token: "gho_s1"}, "")
	if got := p.Speaks(); !slices.Equal(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks %v", got)
	}
	if p.Responses != copilotBase || p.Anthropic != copilotBase || p.Chat != copilotBase {
		t.Fatalf("bases: %q %q %q", p.Chat, p.Responses, p.Anthropic)
	}
}

func TestS1CopilotModelAPIs(t *testing.T) {
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"data":[
		  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/responses","/chat/completions","/websocket"]},
		  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/v1/messages","/chat/completions"]},
		  {"id":"old-model","name":"Old","model_picker_enabled":true,"capabilities":{"type":"chat"}},
		  {"id":"emb-only","name":"Emb","model_picker_enabled":true,"capabilities":{"type":"chat"},
		   "supported_endpoints":["/embeddings"]}]}`))
	}))
	defer models.Close()
	p := s1Copilot(t, models.URL)

	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"gpt-6-sol": {"responses", "chat"},
		"claude-x":  {"anthropic", "chat"},
		"old-model": nil,
		"emb-only":  nil,
	}
	if len(ms) != len(want) {
		t.Fatalf("models: %+v", ms)
	}
	for _, m := range ms {
		if !slices.Equal(m.APIs, want[m.ID]) {
			t.Errorf("%s APIs = %v, want %v", m.ID, m.APIs, want[m.ID])
		}
	}

	// the same answer, read back from the saved live list
	if got := p.APIs("gpt-6-sol"); !slices.Equal(got, []Protocol{Responses, Chat}) {
		t.Errorf("Provider.APIs(gpt-6-sol) = %v", got)
	}
	if got := p.APIs("claude-x"); !slices.Equal(got, []Protocol{Anthropic, Chat}) {
		t.Errorf("Provider.APIs(claude-x) = %v", got)
	}
	if got := p.APIs("old-model"); got != nil {
		t.Errorf("Provider.APIs(old-model) = %v, want nil", got)
	}
	if got := p.APIs("not-listed"); got != nil {
		t.Errorf("Provider.APIs(not-listed) = %v, want nil", got)
	}
	if _, _, ok := catalog.Live("copilot"); !ok {
		t.Error("live list not saved")
	}
}
