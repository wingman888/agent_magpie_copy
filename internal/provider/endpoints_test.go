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

func TestAPIsFromLiveList(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := catalog.SaveLive("acme", "https://acme.test", []catalog.Model{
		{ID: "gpt-x", APIs: []string{"responses", "bogus"}},
		{ID: "claude-x", APIs: []string{"chat", "anthropic", "chat"}},
		{ID: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "acme"}
	if got := p.APIs("gpt-x"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x: %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x: %v", got)
	}
	if got := p.APIs("unknown"); got != nil {
		t.Fatalf("undeclared model should be nil: %v", got)
	}
	if got := p.APIs("missing"); got != nil {
		t.Fatalf("unlisted model should be nil: %v", got)
	}
	if got := (Provider{ID: "nobody"}).APIs("gpt-x"); got != nil {
		t.Fatalf("no list should be nil: %v", got)
	}
}

func TestCopilotAPIsMapping(t *testing.T) {
	got := copilotAPIs([]string{"/chat/completions", "ws:/responses", "/responses", "/v1/messages", "/embeddings", "/v1/messages"})
	if !reflect.DeepEqual(got, []string{"chat", "responses", "anthropic"}) {
		t.Fatalf("mapping: %v", got)
	}
	if got := copilotAPIs(nil); got != nil {
		t.Fatalf("missing list should be unknown: %v", got)
	}
	if got := copilotAPIs([]string{"ws:/responses"}); got != nil {
		t.Fatalf("only ignored endpoints should be unknown: %v", got)
	}
}

func TestCopilotTurnRules(t *testing.T) {
	const img = `{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}`
	cases := []struct {
		name   string
		proto  Protocol
		body   string
		user   bool
		vision bool
	}{
		{"chat user", Chat, `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"chat tool last", Chat, `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, false, false},
		{"chat image", Chat, `{"messages":[{"role":"user","content":[{"type":"text","text":"a"},` + img + `]}]}`, true, true},
		{"chat word in text is not an image", Chat, `{"messages":[{"role":"user","content":"the image_url field"}]}`, true, false},
		{"anthropic plain user", Anthropic, `{"messages":[{"role":"user","content":"hi"}]}`, true, false},
		{"anthropic only tool_result", Anthropic, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"1"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`, false, false},
		{"anthropic tool_result mixed with text", Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"},{"type":"text","text":"and also"}]}]}`, true, false},
		{"anthropic image block", Anthropic, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","data":"AA"}}]}]}`, true, true},
		{"anthropic image inside tool_result", Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":[{"type":"image","source":{}}]}]}]}`, false, true},
		{"responses string input", Responses, `{"input":"hi"}`, true, false},
		{"responses user message", Responses, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true, false},
		{"responses function output", Responses, `{"input":[{"type":"message","role":"user","content":"hi"},{"type":"function_call","call_id":"c"},{"type":"function_call_output","call_id":"c","output":"ok"}]}`, false, false},
		{"responses input_image", Responses, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_image","image_url":"data:image/png;base64,AA"}]}]}`, true, true},
		{"responses empty", Responses, `{"input":[]}`, false, false},
	}
	for _, c := range cases {
		user, vision := copilotTurn(c.proto, []byte(c.body))
		if user != c.user || vision != c.vision {
			t.Errorf("%s: user=%v vision=%v, want user=%v vision=%v", c.name, user, vision, c.user, c.vision)
		}
	}
}

func TestCopilotDeclaresThreeEndpoints(t *testing.T) {
	p := copilotProvider(copilotApp{User: "octo", Token: "gho_decl"}, "")
	if p.Base(Chat) != copilotBase || p.Base(Responses) != copilotBase || p.Base(Anthropic) != copilotBase {
		t.Fatalf("endpoints: %q %q %q", p.Base(Chat), p.Base(Responses), p.Base(Anthropic))
	}
	if got := p.Speaks(); !reflect.DeepEqual(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks: %v", got)
	}
}

// Copilot's model list reads supported_endpoints into APIs; a model without
// the field stays unknown, and the list is cached with it.
func TestCopilotModelsReadSupportedEndpoints(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			w.WriteHeader(404)
			return
		}
		w.Write([]byte(`{"data":[
		  {"id":"gpt-6-sol","name":"GPT-6 Sol","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["ws:/responses","/responses"]},
		  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages","/embeddings"]},
		  {"id":"old-model","name":"Old","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess-endp", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	app := copilotApp{User: "octo", Token: "gho_endp"}
	p := copilotProvider(app, "")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]Protocol{
		"gpt-6-sol": {Responses},
		"claude-x":  {Chat, Anthropic},
		"old-model": nil,
	}
	if len(ms) != 3 {
		t.Fatalf("models: %+v", ms)
	}
	for _, m := range ms {
		if got := p.APIs(m.ID); !reflect.DeepEqual(got, want[m.ID]) {
			t.Fatalf("%s apis: %v want %v", m.ID, got, want[m.ID])
		}
	}
}

// Copilot's Sign labels each turn by its wire API and adds the vision header.
func TestCopilotSignPerTurnHeaders(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess-sign", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": copilotBase}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	p := copilotProvider(copilotApp{User: "octo", Token: "gho_sign"}, "")
	cases := []struct {
		name    string
		proto   Protocol
		path    string
		body    string
		initial string
		vision  bool
	}{
		{"anthropic pure tool_result", Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"}]}]}`, "agent", false},
		{"anthropic mixed text", Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"ok"},{"type":"text","text":"more"}]}]}`, "user", false},
		{"responses string input", Responses, "/responses", `{"input":"hello"}`, "user", false},
		{"responses function output", Responses, "/responses", `{"input":[{"type":"function_call_output","call_id":"c","output":"ok"}]}`, "agent", false},
		{"responses image", Responses, "/responses", `{"input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA"}]}]}`, "user", true},
		{"anthropic image", Anthropic, "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`, "user", true},
		{"chat tool last", Chat, "/chat/completions", `{"messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`, "agent", false},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("POST", p.Base(c.proto)+c.path, nil)
		if err := p.Sign(context.Background(), req, c.proto, []byte(c.body)); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := req.Header.Get("X-Initiator"); got != c.initial {
			t.Errorf("%s: X-Initiator %q want %q", c.name, got, c.initial)
		}
		if got := req.Header.Get("Copilot-Vision-Request") == "true"; got != c.vision {
			t.Errorf("%s: vision header %v want %v", c.name, got, c.vision)
		}
	}
}
