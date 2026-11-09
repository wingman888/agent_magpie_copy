package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gmtDecode decodes a JSON object, failing the test if it is not one.
func gmtDecode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	return m
}

// gmtFirstCandidate returns candidates[0].content.parts and finishReason.
func gmtFirstCandidate(t *testing.T, m map[string]any) ([]any, string) {
	t.Helper()
	cands, _ := m["candidates"].([]any)
	if len(cands) == 0 {
		t.Fatalf("no candidates: %v", m)
	}
	c := cands[0].(map[string]any)
	parts, _ := c["content"].(map[string]any)["parts"].([]any)
	fr, _ := c["finishReason"].(string)
	return parts, fr
}

// gmtUpstreamMessages decodes the Chat request the fake upstream received.
func gmtUpstreamMessages(t *testing.T, f *fake) (map[string]any, []any) {
	t.Helper()
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v\n%s", err, f.got)
	}
	msgs, _ := up["messages"].([]any)
	return up, msgs
}

const gmtChatText = `data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hi "}}]}`

func TestGeminiGenerateContentToChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		gmtChatText,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"maxOutputTokens":100,"temperature":0.5,"topP":0.9,"stopSequences":["END"],
	    "thinkingConfig":{"thinkingBudget":-1}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := gmtDecode(t, body)
	parts, finish := gmtFirstCandidate(t, m)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Hi there" || finish != "STOP" {
		t.Errorf("reply parts %v finish %s", parts, finish)
	}
	um := m["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["candidatesTokenCount"] != float64(5) {
		t.Errorf("usage %v", um)
	}
	if m["modelVersion"] == nil || m["responseId"] == nil {
		t.Errorf("missing modelVersion/responseId: %s", body)
	}

	up, msgs := gmtUpstreamMessages(t, f)
	if f.path != "/v1/chat/completions" || up["stream"] != true {
		t.Errorf("upstream path %s stream %v", f.path, up["stream"])
	}
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "be brief" {
		t.Errorf("system message %v", sys)
	}
	call := msgs[2].(map[string]any)
	calls, _ := call["tool_calls"].([]any)
	if call["role"] != "assistant" || len(calls) != 1 {
		t.Fatalf("assistant message %v", call)
	}
	id := calls[0].(map[string]any)["id"]
	if id == "" || id == nil {
		t.Errorf("generated call id missing: %v", calls[0])
	}
	if args := calls[0].(map[string]any)["function"].(map[string]any)["arguments"]; args != `{"path":"x"}` {
		t.Errorf("call args %v", args)
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != id || tool["content"] != "package x" {
		t.Errorf("tool message %v, want id %v", tool, id)
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	params := fn["parameters"].(map[string]any)
	if params["type"] != "object" || params["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("tool schema not lower-cased: %v", params)
	}
	tc := up["tool_choice"].(map[string]any)
	if tc["type"] != "function" || tc["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool_choice %v", up["tool_choice"])
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) || up["temperature"] != 0.5 || up["top_p"] != 0.9 {
		t.Errorf("generation fields: %v", up)
	}
	if stop, _ := up["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop %v", up["stop"])
	}
}

func TestGeminiStreamOrderAndUsage(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":7,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse",
		`{"contents":[{"role":"user","parts":[{"text":"read a.go"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	// thought, two texts, the whole function call (sent when its arguments
	// are complete, at the stop), and the final event
	if len(evs) != 5 {
		t.Fatalf("want 5 events, got %d: %s", len(evs), body)
	}
	part := func(i int) map[string]any {
		return evs[i]["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	}
	if part(0)["text"] != "hmm" || part(0)["thought"] != true {
		t.Errorf("event 0 %v", part(0))
	}
	if part(1)["text"] != "Hi " || part(2)["text"] != "there" {
		t.Errorf("text events %v %v", part(1), part(2))
	}
	fc := part(3)["functionCall"].(map[string]any)
	if fc["id"] != "call_1" || fc["name"] != "read" || fc["args"].(map[string]any)["path"] != "a.go" {
		t.Errorf("function call %v", fc)
	}
	last := evs[4]
	parts, finish := gmtFirstCandidate(t, last)
	if len(parts) != 0 || finish != "STOP" {
		t.Errorf("final event parts %v finish %s", parts, finish)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(10) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage %v", um)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["stream"] != true {
		t.Errorf("upstream was not streamed: %v", up["stream"])
	}
}

func TestGeminiAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":10}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent",
		`{"systemInstruction":{"parts":[{"text":"be brief"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}],
		  "generationConfig":{"maxOutputTokens":77}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	parts, finish := gmtFirstCandidate(t, gmtDecode(t, body))
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Hello" || finish != "STOP" {
		t.Errorf("reply %s", body)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	if up["system"] != "be brief" || up["max_tokens"] != float64(77) {
		t.Errorf("anthropic upstream body %s", f.got)
	}
}

func TestGeminiJSONModeInstruction(t *testing.T) {
	f := &fake{t: t, reply: sse(gmtChatText, `data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"n?"}]}],
	  "generationConfig":{"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	_, msgs := gmtUpstreamMessages(t, f)
	sys := msgs[0].(map[string]any)
	content, _ := sys["content"].(string)
	if sys["role"] != "system" || !strings.Contains(content, "JSON") || !strings.Contains(content, `"type":"object"`) || !strings.Contains(content, `"type":"integer"`) {
		t.Errorf("system message %v", sys)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	for _, body := range []string{
		`{"contents":[{"role":"user","parts":[{"text":"hello world, this is a test"}]}]}`,
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello world, this is a test"}]}]}}`,
	} {
		code, out := post(t, "/v1beta/models/fake/m1:countTokens", body)
		if code != 200 {
			t.Fatalf("status %d: %s", code, out)
		}
		m := gmtDecode(t, out)
		if n, ok := m["totalTokens"].(float64); !ok || n <= 0 {
			t.Errorf("countTokens %s", out)
		}
	}
}

func TestGeminiModelList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1beta/models", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	m := gmtDecode(t, rec.Body.String())
	models := m["models"].([]any)
	found := false
	for _, x := range models {
		e := x.(map[string]any)
		if e["name"] == "models/fake/m1" {
			found = true
			if e["supportedGenerationMethods"] == nil {
				t.Errorf("no supportedGenerationMethods: %v", e)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", rec.Body.String())
	}
}

func TestGeminiUnknownModelAndPaths(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope:generateContent", `{"contents":[]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) || !strings.Contains(body, `"code":404`) {
		t.Errorf("unknown model: %d %s", code, body)
	}
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), "v1beta") || !strings.Contains(rec.Body.String(), "generateContent") {
		t.Errorf("info: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/nothing", strings.NewReader("{}")))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "v1beta") {
		t.Errorf("unknown path: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGeminiParseTranslation(t *testing.T) {
	// call and response pairing, attachments, thinking history and the
	// tool-choice and effort mappings, checked on the common form.
	r, err := parseGemini([]byte(`{"contents":[
	  {"role":"user","parts":[{"text":"look"},{"inlineData":{"mimeType":"image/png","data":"AAAA"}},
	     {"inlineData":{"mimeType":"application/pdf","data":"BBBB"}},{"fileData":{"mimeType":"image/jpeg","fileUri":"https://x/i.jpg"}}]},
	  {"role":"model","parts":[{"text":"plan","thought":true},{"functionCall":{"name":"f","args":{"a":1}}},{"functionCall":{"id":"own","name":"g"}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"output":"ok"}}},{"functionResponse":{"id":"own","name":"g","response":{"x":1}}}]}],
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":5000}}}`))
	if err != nil {
		t.Fatal(err)
	}
	u := r.Messages[0].Parts
	if len(u) != 4 || u[1].Kind != Image || u[1].MediaType != "image/png" || u[1].Data != "AAAA" ||
		u[2].Kind != Text || !strings.Contains(u[2].Text, "application/pdf") || u[3].Kind != Image || u[3].URL != "https://x/i.jpg" {
		t.Errorf("user parts %+v", u)
	}
	a := r.Messages[1]
	if a.Role != "assistant" || a.Parts[0].Kind != Thinking || a.Parts[0].Text != "plan" {
		t.Errorf("assistant parts %+v", a.Parts)
	}
	id1, id2 := a.Parts[1].ID, a.Parts[2].ID
	if id1 == "" || id2 != "own" {
		t.Errorf("call ids %q %q", id1, id2)
	}
	tr := r.Messages[2].Parts
	if tr[0].CallID != id1 || tr[1].CallID != "own" || tr[0].Text != "ok" || tr[1].Text != `{"x":1}` {
		t.Errorf("tool results %+v", tr)
	}
	if r.Effort != "medium" || !r.Thinking {
		t.Errorf("effort %q thinking %v", r.Effort, r.Thinking)
	}

	cases := []struct {
		cfg, want string
	}{
		{`{"functionCallingConfig":{"mode":"NONE"}}`, "none"},
		{`{"functionCallingConfig":{"mode":"AUTO"}}`, "auto"},
		{`{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}}`, "name:read"},
		{`{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["a","b"]}}`, "required"},
	}
	for _, c := range cases {
		r, err := parseGemini([]byte(`{"contents":[],"toolConfig":` + c.cfg + `}`))
		if err != nil || r.ToolChoice != c.want {
			t.Errorf("%s: tool choice %q, want %q (%v)", c.cfg, r.ToolChoice, c.want, err)
		}
	}
	for budget, want := range map[string]string{"-1": "medium", "0": "low", "512": "low", "4096": "medium", "20000": "high"} {
		r, _ := parseGemini([]byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"thinkingBudget":` + budget + `}}}`))
		if r.Effort != want {
			t.Errorf("budget %s: effort %q, want %q", budget, r.Effort, want)
		}
	}
}

func TestGeminiUpstreamError(t *testing.T) {
	f := &fake{t: t, ctype: "application/json", code: 400, reply: `{"error":{"message":"bad","type":"x"}}`}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 400 || !strings.Contains(body, `"status":"INVALID_ARGUMENT"`) || !strings.Contains(body, "Fake: bad") {
		t.Errorf("%d %s", code, body)
	}
}
