package provider

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// signCopilot signs a request to Copilot with a body in the given API shape
// and returns the headers it would carry. The session is cached up front so
// no token exchange is made.
func signCopilot(t *testing.T, proto Protocol, path, body string) http.Header {
	t.Helper()
	copilotMu.Lock()
	copilotSessions = map[string]copilotSession{"gho_hdr": {
		Token:     "sess",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	}}
	copilotMu.Unlock()
	p := copilotProvider(copilotApp{Token: "gho_hdr", User: "tester"}, "")
	req, err := http.NewRequest(http.MethodPost, "https://copilot.example"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Sign(context.Background(), req, proto, []byte(body)); err != nil {
		t.Fatal(err)
	}
	return req.Header
}

func TestCopilotInitiatorAnthropic(t *testing.T) {
	// the last user message is nothing but tool results: the agent's turn
	h := signCopilot(t, Anthropic, "/v1/messages", `{"model":"claude-sonnet-5","max_tokens":8,"messages":[
		{"role":"user","content":"run ls"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a b"}]}]}`)
	if h.Get("X-Initiator") != "agent" {
		t.Fatalf("pure tool_result should be agent, got %q", h.Get("X-Initiator"))
	}
	if h.Get("Copilot-Vision-Request") != "" {
		t.Fatalf("no image, but vision header set")
	}

	// tool results mixed with text: the user spoke
	h = signCopilot(t, Anthropic, "/v1/messages", `{"model":"claude-sonnet-5","max_tokens":8,"messages":[
		{"role":"user","content":"run ls"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"a b"},{"type":"text","text":"thanks, now cat it"}]}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Fatalf("tool_result mixed with text should be user, got %q", h.Get("X-Initiator"))
	}

	// a plain user turn
	h = signCopilot(t, Anthropic, "/v1/messages", `{"model":"claude-sonnet-5","max_tokens":8,"messages":[
		{"role":"user","content":"hi"}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Fatalf("plain user turn should be user, got %q", h.Get("X-Initiator"))
	}
}

func TestCopilotInitiatorResponses(t *testing.T) {
	// a string input is the user's text
	h := signCopilot(t, Responses, "/responses", `{"model":"gpt-6-sol","input":"hello"}`)
	if h.Get("X-Initiator") != "user" {
		t.Fatalf("string input should be user, got %q", h.Get("X-Initiator"))
	}

	// a function output last is the agent's turn
	h = signCopilot(t, Responses, "/responses", `{"model":"gpt-6-sol","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"ls"}]},
		{"type":"function_call","call_id":"c1","name":"bash","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"a b"}]}`)
	if h.Get("X-Initiator") != "agent" {
		t.Fatalf("function output should be agent, got %q", h.Get("X-Initiator"))
	}

	// a user message last is the user's turn
	h = signCopilot(t, Responses, "/responses", `{"model":"gpt-6-sol","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Fatalf("user message should be user, got %q", h.Get("X-Initiator"))
	}
}

func TestCopilotInitiatorChat(t *testing.T) {
	h := signCopilot(t, Chat, "/chat/completions", `{"model":"claude-sonnet-5","messages":[
		{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`)
	if h.Get("X-Initiator") != "agent" {
		t.Fatalf("tool message should be agent, got %q", h.Get("X-Initiator"))
	}
	h = signCopilot(t, Chat, "/chat/completions", `{"model":"claude-sonnet-5","messages":[
		{"role":"assistant","content":"ok"},{"role":"user","content":"hi"}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Fatalf("user message should be user, got %q", h.Get("X-Initiator"))
	}
}

func TestCopilotVisionHeader(t *testing.T) {
	cases := []struct {
		name  string
		proto Protocol
		path  string
		body  string
	}{
		{"chat image", Chat, "/chat/completions", `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`},
		{"responses input_image", Responses, "/responses", `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"what"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`},
		{"anthropic image", Anthropic, "/v1/messages", `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"text","text":"what"}]}]}`},
		{"anthropic image in tool_result", Anthropic, "/v1/messages", `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}]}`},
	}
	for _, c := range cases {
		h := signCopilot(t, c.proto, c.path, c.body)
		if h.Get("Copilot-Vision-Request") != "true" {
			t.Fatalf("%s: vision header missing: %v", c.name, h)
		}
	}

	// text that merely mentions the image types is not an image
	h := signCopilot(t, Chat, "/chat/completions", `{"model":"m","messages":[{"role":"user","content":"the image_url field and input_image type"}]}`)
	if h.Get("Copilot-Vision-Request") != "" {
		t.Fatalf("text mentioning image types should not set vision header")
	}
}
