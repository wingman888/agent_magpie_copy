package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// cacheCopilotSession puts one session into the shared cache under a single
// key and restores that key's previous state when the test ends, so other
// tests' entries are left alone.
func cacheCopilotSession(t *testing.T, github string, s copilotSession) {
	t.Helper()
	copilotMu.Lock()
	if copilotSessions == nil {
		copilotSessions = map[string]copilotSession{}
	}
	old, had := copilotSessions[github]
	copilotSessions[github] = s
	copilotMu.Unlock()
	t.Cleanup(func() {
		copilotMu.Lock()
		defer copilotMu.Unlock()
		if had {
			copilotSessions[github] = old
		} else {
			delete(copilotSessions, github)
		}
	})
}

// signCopilot signs a request to Copilot with a body in the given API shape
// and returns the headers it would carry. The session is cached up front so
// no token exchange is made.
func signCopilot(t *testing.T, proto Protocol, path, body string) http.Header {
	t.Helper()
	cacheCopilotSession(t, "gho_hdr", copilotSession{
		Token:     "sess",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
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

func TestCopilotInitiatorChatLastTurn(t *testing.T) {
	// the last message is an assistant reply: not the user's turn, even
	// though an earlier user message has array content
	h := signCopilot(t, Chat, "/chat/completions", `{"model":"m","messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"tool","content":"x"},{"role":"assistant","content":"ok"}]}`)
	if h.Get("X-Initiator") != "agent" {
		t.Fatalf("[user, tool, assistant] should be agent, got %q", h.Get("X-Initiator"))
	}

	// [user, tool] ends with the tool reply: the agent's turn
	h = signCopilot(t, Chat, "/chat/completions", `{"model":"m","messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"tool","content":"x"}]}`)
	if h.Get("X-Initiator") != "agent" {
		t.Fatalf("[user, tool] should be agent, got %q", h.Get("X-Initiator"))
	}

	// a trailing system message does not change whose turn it is
	h = signCopilot(t, Chat, "/chat/completions", `{"model":"m","messages":[
		{"role":"user","content":"hi"},{"role":"system","content":"be brief"}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Fatalf("user followed by system should be user, got %q", h.Get("X-Initiator"))
	}

	// trailing system and developer messages are skipped; the real last turn decides
	h = signCopilot(t, Chat, "/chat/completions", `{"model":"m","messages":[
		{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"system","content":"s"},{"role":"developer","content":"d"}]}`)
	if h.Get("X-Initiator") != "agent" {
		t.Fatalf("assistant then system/developer should be agent, got %q", h.Get("X-Initiator"))
	}
}

func TestCopilotInitiatorEmptyContent(t *testing.T) {
	// A user message with no content blocks stays the user's turn: only a
	// message made of tool_result blocks is a tool's reply.
	for name, body := range map[string]string{
		"anthropic empty string": `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":""}]}`,
		"anthropic empty array":  `{"model":"m","max_tokens":8,"messages":[{"role":"user","content":[]}]}`,
		"anthropic no content":   `{"model":"m","max_tokens":8,"messages":[{"role":"user"}]}`,
		"chat no content":        `{"model":"m","messages":[{"role":"assistant","content":"ok"},{"role":"user"}]}`,
	} {
		proto := Anthropic
		path := "/v1/messages"
		if strings.HasPrefix(name, "chat") {
			proto, path = Chat, "/chat/completions"
		}
		h := signCopilot(t, proto, path, body)
		if h.Get("X-Initiator") != "user" {
			t.Fatalf("%s: should be user, got %q", name, h.Get("X-Initiator"))
		}
	}
}

func TestCopilotInitiatorResponsesShapes(t *testing.T) {
	// a function call last, with no role, is the agent's turn
	h := signCopilot(t, Responses, "/responses", `{"model":"m","input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"ls"}]},
		{"type":"function_call","call_id":"c1","name":"bash","arguments":"{}"}]}`)
	if h.Get("X-Initiator") != "agent" {
		t.Fatalf("function_call last should be agent, got %q", h.Get("X-Initiator"))
	}

	// a message with only role:"user" and no type is still the user's turn
	h = signCopilot(t, Responses, "/responses", `{"model":"m","input":[{"role":"user","content":"hi"}]}`)
	if h.Get("X-Initiator") != "user" {
		t.Fatalf("role-only user message should be user, got %q", h.Get("X-Initiator"))
	}
}

func TestCopilotMalformedBodies(t *testing.T) {
	// none of these may panic; each falls back to the default (agent) turn
	for name, body := range map[string]string{
		"not json":          `not json`,
		"empty body":        ``,
		"json array":        `[1,2]`,
		"messages not list": `{"model":"m","messages":"hello"}`,
		"messages object":   `{"model":"m","messages":{"role":"user"}}`,
		"input number":      `{"model":"m","input":5}`,
		"input object":      `{"model":"m","input":{"type":"message"}}`,
		"input bad items":   `{"model":"m","input":[1,"x"]}`,
	} {
		h := signCopilot(t, Responses, "/responses", body)
		if x := h.Get("X-Initiator"); x != "agent" {
			t.Fatalf("%s: expected default agent initiator, got %q", name, x)
		}
		if h.Get("Copilot-Vision-Request") != "" {
			t.Fatalf("%s: unexpected vision header", name)
		}
	}
}

func TestCopilotVisionIgnoresNonContentPositions(t *testing.T) {
	// an image type named in tool schemas, metadata or other non-content places
	// must not set the vision header
	cases := []struct {
		name  string
		proto Protocol
		path  string
		body  string
	}{
		{"anthropic tool schema", Anthropic, "/v1/messages", `{"model":"m","max_tokens":8,"tools":[{"name":"draw","input_schema":{"type":"object","properties":{"kind":{"type":"image"}}}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"chat tool schema", Chat, "/chat/completions", `{"model":"m","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"x":{"type":"image_url"}}}}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"responses metadata", Responses, "/responses", `{"model":"m","metadata":{"part":{"type":"input_image"}},"input":"hi"}`},
		{"responses tool schema", Responses, "/responses", `{"model":"m","tools":[{"type":"function","parameters":{"type":"image"}}],"input":"hi"}`},
	}
	for _, c := range cases {
		h := signCopilot(t, c.proto, c.path, c.body)
		if h.Get("Copilot-Vision-Request") != "" {
			t.Fatalf("%s: vision header set for a non-content image type", c.name)
		}
	}
}
