package gateway

import (
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gfixReasoningReply is an OpenAI Chat stream with reasoning followed by text.
var gfixReasoningReply = sse(
	`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{"content":"ok"}}]}`,
	`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
	`data: [DONE]`)

// gfixToolBody declares three functions and offers only two under ANY.
const gfixToolBody = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
  "tools":[{"functionDeclarations":[
    {"name":"a","parameters":{"type":"OBJECT"}},
    {"name":"b","parameters":{"type":"OBJECT"}},
    {"name":"c","parameters":{"type":"OBJECT"}}]}],
  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["a","b"]}}}`

func gfixToolNames(r *Request) []string {
	var out []string
	for _, t := range r.Tools {
		out = append(out, t.Name)
	}
	return out
}

func TestGFixAnyWithSeveralNamesOffersOnlyThose(t *testing.T) {
	r, err := parseGemini([]byte(gfixToolBody))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(gfixToolNames(r), ","); got != "a,b" {
		t.Errorf("tools = %s, want a,b", got)
	}
	if r.ToolChoice != "required" {
		t.Errorf("tool choice = %q, want required", r.ToolChoice)
	}
}

func TestGFixAnyWithOneNamePinsThatFunction(t *testing.T) {
	body := strings.Replace(gfixToolBody, `["a","b"]`, `["b"]`, 1)
	r, err := parseGemini([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if r.ToolChoice != "name:b" {
		t.Errorf("tool choice = %q, want name:b", r.ToolChoice)
	}
}

func TestGFixIncludeThoughtsFalseHidesReasoningUnary(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":1024,"includeThoughts":false}}}`
	f := &fake{t: t, reply: gfixReasoningReply}
	setup(t, provider.Chat, f)
	code, out := post(t, "/v1beta/models/fake/m1:generateContent", body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	for _, p := range gemHTTPParts(gemHTTPEvents2(t, out)) {
		if p["thought"] == true {
			t.Errorf("reasoning returned although includeThoughts is false: %s", out)
		}
	}
}

func TestGFixIncludeThoughtsFalseHidesReasoningStream(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":1024,"includeThoughts":false}}}`
	f := &fake{t: t, reply: gfixReasoningReply}
	setup(t, provider.Chat, f)
	code, out := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	var text string
	for _, p := range gemHTTPParts(gemHTTPEvents(t, out)) {
		if p["thought"] == true {
			t.Errorf("reasoning streamed although includeThoughts is false: %s", out)
		}
		if s, ok := p["text"].(string); ok {
			text += s
		}
	}
	if text != "ok" {
		t.Errorf("text = %q, want ok", text)
	}
}

func TestGFixStreamErrorEndsWithoutStop(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"partial"}}]}`,
		`data: {"error":{"message":"upstream exploded"}}`,
	)}
	setup(t, provider.Chat, f)
	code, out := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, out)
	}
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "upstream exploded") {
		t.Errorf("error event missing: %s", out)
	}
	if strings.Contains(out, "finishReason") {
		t.Errorf("stream ended with a finish reason after an error: %s", out)
	}
}
