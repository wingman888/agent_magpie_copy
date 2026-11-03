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
)

func TestCopilotProtocolFromPath(t *testing.T) {
	for path, want := range map[string]Protocol{
		"/chat/completions":         Chat,
		"/responses":                Responses,
		"/v1/messages":              Anthropic,
		"/v1/messages/count_tokens": Anthropic,
	} {
		if got := copilotProtocol(path); got != want {
			t.Errorf("%s: %s, want %s", path, got, want)
		}
	}
}

func TestCopilotUserTurn(t *testing.T) {
	cases := []struct {
		name  string
		proto Protocol
		body  string
		want  bool
	}{
		{"chat last user", Chat, `{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`, true},
		{"chat tool reply", Chat, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null},{"role":"tool","content":"x"}]}`, false},
		{"chat assistant last", Chat, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`, false},
		{"chat no messages", Chat, `{}`, false},

		{"anthropic text user", Anthropic, `{"messages":[{"role":"user","content":"hi"}]}`, true},
		{"anthropic string content", Anthropic, `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, true},
		{"anthropic only tool_result", Anthropic, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ls","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`, false},
		{"anthropic tool_result mixed with text", Anthropic, `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"also this"}]}]}`, true},
		{"anthropic last assistant", Anthropic, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"yo"}]}`, false},

		{"responses string input", Responses, `{"input":"hello"}`, true},
		{"responses user message", Responses, `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, true},
		{"responses untyped user message", Responses, `{"input":[{"role":"user","content":"hi"}]}`, true},
		{"responses function output only", Responses, `{"input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, false},
		{"responses user then function output", Responses, `{"input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c1","output":"ok"}]}`, true},
		{"responses assistant last", Responses, `{"input":[{"role":"user","content":"hi"},{"type":"message","role":"assistant","content":"yo"}]}`, false},
	}
	for _, c := range cases {
		if got := copilotUserTurn(c.proto, []byte(c.body)); got != c.want {
			t.Errorf("%s: user turn %v, want %v", c.name, got, c.want)
		}
	}
	if copilotUserTurn(Chat, nil) {
		t.Error("empty body counted as a user turn")
	}
}

func TestCopilotHasImage(t *testing.T) {
	cases := []struct {
		name  string
		proto Protocol
		body  string
		want  bool
	}{
		{"chat image", Chat, `{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`, true},
		{"chat text mentions image_url", Chat, `{"messages":[{"role":"user","content":"the image_url field"}]}`, false},
		{"responses input_image", Responses, `{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA"}]}]}`, true},
		{"responses text only", Responses, `{"input":"hi"}`, false},
		{"anthropic image", Anthropic, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}`, true},
		{"anthropic no image", Anthropic, `{"messages":[{"role":"user","content":"hi"}]}`, false},
	}
	for _, c := range cases {
		if got := copilotHasImage(c.proto, []byte(c.body)); got != c.want {
			t.Errorf("%s: vision %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCopilotAPIsMapping(t *testing.T) {
	got := copilotAPIs([]string{"/responses", "ws:/responses", "/v1/messages", "/chat/completions", "/embeddings", "/responses"})
	if !reflect.DeepEqual(got, []string{"responses", "anthropic", "chat"}) {
		t.Fatalf("apis: %v", got)
	}
	if copilotAPIs(nil) != nil || copilotAPIs([]string{"/embeddings", "ws:/responses"}) != nil {
		t.Fatal("unknown endpoints should give nil APIs")
	}
}

// copilotTestAPI serves Copilot's model list and records the headers of
// every other request, through the same sign-in the other Copilot tests use.
func copilotTestAPI(t *testing.T) (Provider, *http.Header) {
	t.Helper()
	signIn(t)
	seen := &http.Header{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"gpt-x","name":"GPT X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/responses"]},
			  {"id":"claude-x","name":"Claude X","model_picker_enabled":true,"capabilities":{"type":"chat"},"supported_endpoints":["/chat/completions","/v1/messages","/embeddings"]},
			  {"id":"legacy","name":"Legacy","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
			return
		}
		*seen = r.Header.Clone()
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(api.Close)
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	t.Cleanup(tokens.Close)
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	t.Cleanup(func() { CopilotTokenURL = old })
	copilotSessions = map[string]copilotSession{}

	p, ok := find(All(), "copilot")
	if !ok {
		t.Fatal("no copilot provider")
	}
	return p, seen
}

func TestCopilotDeclaresThreeEndpoints(t *testing.T) {
	p, _ := copilotTestAPI(t)
	if p.Chat != copilotBase || p.Responses != copilotBase || p.Anthropic != copilotBase {
		t.Fatalf("bases: chat=%q responses=%q anthropic=%q", p.Chat, p.Responses, p.Anthropic)
	}
	if got := p.Speaks(); !reflect.DeepEqual(got, []Protocol{Chat, Responses, Anthropic}) {
		t.Fatalf("speaks: %v", got)
	}
}

func TestCopilotModelAPIsFromList(t *testing.T) {
	p, _ := copilotTestAPI(t)
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := p.APIs("gpt-x"); !reflect.DeepEqual(got, []Protocol{Responses}) {
		t.Fatalf("gpt-x APIs: %v", got)
	}
	if got := p.APIs("claude-x"); !reflect.DeepEqual(got, []Protocol{Chat, Anthropic}) {
		t.Fatalf("claude-x APIs: %v", got)
	}
	if got := p.APIs("legacy"); got != nil {
		t.Fatalf("legacy (no supported_endpoints) should be unknown, got %v", got)
	}
	if got := p.APIs("not-listed"); got != nil {
		t.Fatalf("unlisted model APIs: %v", got)
	}
}

func TestCopilotSignHeaders(t *testing.T) {
	p, seen := copilotTestAPI(t)
	ctx := context.Background()
	send := func(base, path, body string) http.Header {
		t.Helper()
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		if err := p.Sign(ctx, req, Anthropic, []byte(body)); err != nil {
			t.Fatal(err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return *seen
	}
	toolOnly := `{"model":"claude-x","messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ls","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`
	mixed := `{"model":"claude-x","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"and then"}]}]}`
	image := `{"model":"claude-x","messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]}]}`

	if h := send(p.Anthropic, "/v1/messages", toolOnly); h.Get("X-Initiator") != "agent" || h.Get("Copilot-Vision-Request") != "" {
		t.Fatalf("tool_result turn: %v", h)
	}
	if h := send(p.Anthropic, "/v1/messages", mixed); h.Get("X-Initiator") != "user" {
		t.Fatalf("mixed turn: %v", h)
	}
	if h := send(p.Anthropic, "/v1/messages", image); h.Get("X-Initiator") != "user" || h.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("image turn: %v", h)
	}
	if h := send(p.Responses, "/responses", `{"model":"gpt-x","input":"hello"}`); h.Get("X-Initiator") != "user" {
		t.Fatalf("responses string input: %v", h)
	}
	if h := send(p.Responses, "/responses", `{"model":"gpt-x","input":[{"type":"input_image","image_url":"x"}]}`); h.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("responses image: %v", h)
	}
	if h := send(p.Responses, "/responses", `{"model":"gpt-x","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"ok"}]}`); h.Get("X-Initiator") != "agent" {
		t.Fatalf("responses user then output: %v", h)
	}
	// a user message after the output ends the turn: the user's, and a run of
		// outputs after an earlier message is still the agent's
		if h := send(p.Responses, "/responses", `{"model":"gpt-x","input":[{"type":"function_call_output","call_id":"c","output":"ok"},{"role":"user","content":"and then"}]}`); h.Get("X-Initiator") != "user" {
			t.Fatalf("responses output then user: %v", h)
		}
		if h := send(p.Responses, "/responses", `{"model":"gpt-x","input":[{"role":"user","content":"hi"},{"type":"function_call_output","call_id":"c","output":"ok"},{"type":"function_call_output","call_id":"d","output":"ok"}]}`); h.Get("X-Initiator") != "agent" {
			t.Fatalf("responses user then two outputs: %v", h)
		}
		if h := send(p.Responses, "/responses", `{"model":"gpt-x","input":[{"type":"function_call_output","call_id":"c","output":"ok"}]}`); h.Get("X-Initiator") != "agent" {
		t.Fatalf("responses output only: %v", h)
	}
	chatBody := `{"model":"claude-x","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`
	if h := send(p.Chat, "/chat/completions", chatBody); h.Get("X-Initiator") != "user" || h.Get("Copilot-Vision-Request") != "true" {
		t.Fatalf("chat image: %v", h)
	}
	if h := send(p.Chat, "/chat/completions", `{"messages":[{"role":"user","content":"a"},{"role":"tool","content":"b"}]}`); h.Get("X-Initiator") != "agent" || h.Get("Copilot-Vision-Request") != "" {
		t.Fatalf("chat tool reply: %v", h)
	}
}
