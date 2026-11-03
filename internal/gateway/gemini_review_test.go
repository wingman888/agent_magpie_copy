package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// Anthropic refuses a forced tool choice while thinking is on: the forced
// choice must fall back to auto, and the tools must still be sent.
func TestAnthropicForcedChoiceDowngradedWithThinking(t *testing.T) {
	tools := []Tool{{Name: "read", Schema: []byte(`{"type":"object"}`)}}
	cases := []struct {
		name     string
		choice   string
		effort   string
		want     string // tool_choice type; "" means absent (auto is the default, so it is left out)
		thinking bool
	}{
		{"any with thinking", "required", "medium", "", true},
		{"named with thinking", "name:read", "low", "", true},
		{"none with thinking", "none", "medium", "none", true},
		{"any without thinking", "required", "", "any", false},
		{"named without thinking", "name:read", "", "tool", false},
	}
	for _, c := range cases {
		r := &Request{Messages: []Message{{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}}},
			Tools: tools, ToolChoice: c.choice, Effort: c.effort}
		var up map[string]any
		if err := json.Unmarshal(buildAnthropic(r, "m1"), &up); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if _, ok := up["thinking"]; ok != c.thinking {
			t.Errorf("%s: thinking present=%v, want %v: %v", c.name, ok, c.thinking, up)
		}
		if up["tools"] == nil {
			t.Errorf("%s: tools dropped: %v", c.name, up)
		}
		tc, _ := up["tool_choice"].(map[string]any)
		got := ""
		if tc != nil {
			got, _ = tc["type"].(string)
		}
		if got != c.want {
			t.Errorf("%s: tool_choice %q, want %q: %v", c.name, got, c.want, up)
		}
		if c.want == "tool" && tc["name"] != "read" {
			t.Errorf("%s: wrong name: %v", c.name, tc)
		}
	}
}

// Through the whole gateway: a Gemini ANY with a thinking budget reaches an
// Anthropic upstream as auto with thinking enabled.
func TestGeminiAnyWithThinkingOverAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":1}}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[{"role":"user","parts":[{"text":"read"}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"OBJECT"}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY"}},
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":2000}}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["thinking"] == nil {
		t.Errorf("thinking not enabled: %s", f.got)
	}
	// absent means auto to Anthropic; a forced type would be rejected with thinking on
	if tc, ok := up["tool_choice"]; ok && tc != nil {
		if m, _ := tc.(map[string]any); m["type"] != "auto" {
			t.Errorf("tool_choice must not force a tool with thinking on: %v", tc)
		}
	}
}

// Cache writes are prompt tokens too, as cache reads already were.
func TestGeminiUsagePromptCountsCacheWrite(t *testing.T) {
	um := geminiUsage(Usage{Input: 10, CacheRead: 5, CacheWrite: 3, Output: 4, Reasoning: 2})
	if um["promptTokenCount"] != 18 || um["candidatesTokenCount"] != 4 || um["totalTokenCount"] != 22 {
		t.Errorf("usage: %v", um)
	}
	if um["cachedContentTokenCount"] != 5 || um["thoughtsTokenCount"] != 2 {
		t.Errorf("usage cache/thoughts: %v", um)
	}
}

func TestGeminiUsageOverAnthropicCountsCacheWrite(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"cache_read_input_tokens":5,"cache_creation_input_tokens":3}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Yo"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	um, _ := decode(t, body)["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(15) || um["candidatesTokenCount"] != float64(3) || um["cachedContentTokenCount"] != float64(5) {
		t.Errorf("usage: %s", body)
	}
}

// A model reference keeps a "models/" prefix as written: it names no model
// here, so it is a 404 rather than being silently stripped to "fake/m1".
func TestGeminiModelRefKeepsModelsPrefix(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/models/fake/m1:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("generateContent: %d %s", code, body)
	}
	code, body = post(t, "/v1beta/models/models/fake/m1:countTokens", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("countTokens: %d %s", code, body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models/models/fake/m1", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"status":"NOT_FOUND"`) {
		t.Errorf("model info: %d %s", rec.Code, rec.Body.String())
	}
}

// includeThoughts false hides reasoning from the reply, but the upstream
// still gets the thinking effort the budget asked for.
func TestGeminiIncludeThoughtsFalseHidesReasoning(t *testing.T) {
	reply := sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":false}}}`

	f := &fake{t: t, reply: reply}
	setup(t, provider.Chat, f)
	code, out := post(t, "/v1beta/models/fake/m1:generateContent", body)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if strings.Contains(out, "hmm") || strings.Contains(out, `"thought"`) {
		t.Errorf("reasoning leaked: %s", out)
	}
	if ps := partList(candidate(t, decode(t, out))); len(ps) != 1 || ps[0]["text"] != "Hi" {
		t.Errorf("parts: %s", out)
	}
	if !strings.Contains(string(f.got), `"reasoning_effort":"medium"`) {
		t.Errorf("upstream lost thinking effort: %s", f.got)
	}

	f2 := &fake{t: t, reply: reply}
	setup(t, provider.Chat, f2)
	code, out = post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", body)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	if strings.Contains(out, "hmm") || strings.Contains(out, `"thought"`) {
		t.Errorf("stream leaked reasoning: %s", out)
	}
	if !strings.Contains(out, "Hi") || !strings.Contains(out, `"finishReason":"STOP"`) {
		t.Errorf("stream lost text or finish: %s", out)
	}
}

func TestGeminiIncludeThoughtsTrueShowsReasoning(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, out := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, out)
	}
	ps := partList(candidate(t, decode(t, out)))
	if len(ps) != 2 || ps[0]["thought"] != true || ps[0]["text"] != "hmm" {
		t.Errorf("parts: %s", out)
	}
}

func TestParseGeminiIncludeThoughtsFlags(t *testing.T) {
	for body, want := range map[string][2]bool{ // {Thinking, HideThoughts}
		`{"generationConfig":{"thinkingConfig":{"includeThoughts":true}}}`:  {true, false},
		`{"generationConfig":{"thinkingConfig":{"includeThoughts":false}}}`: {false, true},
		`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`:     {false, false},
	} {
		r, err := parseGemini([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if r.Thinking != want[0] || r.HideThoughts != want[1] {
			t.Errorf("%s: thinking %v hide %v", body, r.Thinking, r.HideThoughts)
		}
	}
	r, _ := parseGemini([]byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`))
	if r.Effort != "medium" {
		t.Errorf("effort independent of includeThoughts: %q", r.Effort)
	}
}

// ANY naming only functions that are not declared cannot be honoured, so it
// is an explicit 400 rather than a constraint on nothing.
func TestGeminiAnyWithUndeclaredAllowedNames(t *testing.T) {
	_, err := parseGemini([]byte(`{"tools":[{"functionDeclarations":[{"name":"read"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["nope"]}}}`))
	if err == nil {
		t.Fatal("want an error for allowedFunctionNames with no declared match")
	}
	f := &fake{t: t}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "tools":[{"functionDeclarations":[{"name":"read"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["nope"]}}}`)
	if code != 400 || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) {
		t.Errorf("%d %s", code, body)
	}
	if len(f.got) != 0 {
		t.Errorf("upstream should not be called: %s", f.got)
	}

	// one of several names matching is still a constraint on the matching tool
	r, err := parseGemini([]byte(`{"tools":[{"functionDeclarations":[{"name":"read"},{"name":"write"}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read","nope"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.ToolChoice != "required" || len(r.Tools) != 1 || r.Tools[0].Name != "read" {
		t.Errorf("choice %q tools %v", r.ToolChoice, r.Tools)
	}
}
