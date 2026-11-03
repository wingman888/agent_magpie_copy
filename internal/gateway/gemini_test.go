package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// gemGet sends a GET to a fresh gateway.
func gemGet(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec.Code, rec.Body.String()
}

// upstreamBody decodes what the fake upstream received.
func upstreamBody(t *testing.T, f *fake) map[string]any {
	t.Helper()
	var up map[string]any
	if err := json.Unmarshal(f.got, &up); err != nil {
		t.Fatalf("upstream body: %v: %s", err, f.got)
	}
	return up
}

// decodeMap decodes a JSON object reply.
func decodeMap(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("reply is not JSON: %v: %s", err, body)
	}
	return m
}

// candidate returns candidates[0] of a Gemini reply or event.
func candidate(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	cs, ok := m["candidates"].([]any)
	if !ok || len(cs) == 0 {
		t.Fatalf("no candidates: %v", m)
	}
	return cs[0].(map[string]any)
}

// partsOf returns the parts of a candidate.
func partsOf(t *testing.T, cand map[string]any) []map[string]any {
	t.Helper()
	raw := cand["content"].(map[string]any)["parts"].([]any)
	var out []map[string]any
	for _, p := range raw {
		out = append(out, p.(map[string]any))
	}
	return out
}

func TestGeminiGenerateToChatUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","content":"Hi "}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"hello"}]}],
	  "generationConfig":{"maxOutputTokens":100,"temperature":0.2}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	up := upstreamBody(t, f)
	msgs := up["messages"].([]any)
	if sys := msgs[0].(map[string]any); sys["role"] != "system" || sys["content"] != "be brief" {
		t.Errorf("system message: %v", sys)
	}
	if up["max_tokens"] != float64(100) || up["temperature"] != 0.2 {
		t.Errorf("params: %v", up)
	}
	m := decodeMap(t, body)
	cand := candidate(t, m)
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason %v", cand["finishReason"])
	}
	if p := partsOf(t, cand); len(p) != 1 || p[0]["text"] != "Hi there" {
		t.Errorf("parts: %v", p)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(5) {
		t.Errorf("usage: %v", u)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) || strings.Contains(body, `": `) {
		t.Errorf("reply is not compact: %s", body)
	}
}

func TestGeminiStreamOrderAndChatTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"role":"assistant","reasoning_content":"hmm"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":"Let me check"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":8,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["read"]}},
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":-1,"includeThoughts":true}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}

	// what the upstream saw
	up := upstreamBody(t, f)
	if up["stream"] != true || up["reasoning_effort"] != "medium" {
		t.Errorf("stream/effort: %v", up)
	}
	choice := up["tool_choice"].(map[string]any)
	if fn := choice["function"].(map[string]any); choice["type"] != "function" || fn["name"] != "read" {
		t.Errorf("ANY with one allowed function should name it: %v", choice)
	}
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	if sys := msgs[0].(map[string]any); sys["content"] != "be brief" {
		t.Errorf("system content must be a plain string: %v", sys)
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	id, _ := call["id"].(string)
	tool := msgs[3].(map[string]any)
	if id == "" || tool["role"] != "tool" || tool["tool_call_id"] != id || tool["content"] != "package x" {
		t.Errorf("call and result not paired by name: call %v, result %v", call, tool)
	}
	tp := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["parameters"].(map[string]any)
	if tp["type"] != "object" || tp["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema types not lower-cased: %v", tp)
	}

	// what the client got: thought, text, function call, then the finish
	evs := events(body)
	if len(evs) != 4 {
		t.Fatalf("want 4 events, got %d: %s", len(evs), body)
	}
	if p := partsOf(t, candidate(t, evs[0])); len(p) != 1 || p[0]["thought"] != true || p[0]["text"] != "hmm" {
		t.Errorf("thought event: %v", p)
	}
	if p := partsOf(t, candidate(t, evs[1])); len(p) != 1 || p[0]["text"] != "Let me check" {
		t.Errorf("text event: %v", p)
	}
	fc := partsOf(t, candidate(t, evs[2]))[0]["functionCall"].(map[string]any)
	args := fc["args"].(map[string]any)
	if fc["id"] != "call_9" || fc["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("function call event: %v", fc)
	}
	last := candidate(t, evs[3])
	if last["finishReason"] != "STOP" {
		t.Errorf("finish: %v", last)
	}
	u := evs[3]["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(10) || u["candidatesTokenCount"] != float64(8) || u["thoughtsTokenCount"] != float64(3) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiJSONModeAndNoneChoice(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"content":"{\"n\":4}"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "contents":[{"role":"user","parts":[{"text":"give n"}]}],
	  "tools":[{"functionDeclarations":[{"name":"read","parameters":{"type":"OBJECT"}}]}],
	  "toolConfig":{"functionCallingConfig":{"mode":"NONE"}},
	  "generationConfig":{"responseMimeType":"application/json",
	    "responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}}}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	up := upstreamBody(t, f)
	sys := up["messages"].([]any)[0].(map[string]any)
	content, _ := sys["content"].(string)
	if sys["role"] != "system" || !strings.Contains(content, "JSON") || !strings.Contains(content, `"integer"`) || !strings.Contains(content, `"object"`) {
		t.Errorf("JSON instruction with lower-cased schema missing: %v", sys)
	}
	if _, ok := up["response_format"]; ok {
		t.Errorf("no upstream response_format should be set: %v", up)
	}
	if up["tool_choice"] != "none" {
		t.Errorf("NONE should map to none: %v", up["tool_choice"])
	}
	if p := partsOf(t, candidate(t, decodeMap(t, body))); len(p) != 1 || p[0]["text"] != `{"n":4}` {
		t.Errorf("reply text: %v", p)
	}
}

func TestGeminiToResponsesUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello"}`,
		`data: {"type":"response.completed","response":{"id":"r1","model":"m1","status":"completed","usage":{"input_tokens":7,"output_tokens":3}}}`)}
	setup(t, provider.Responses, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":50}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/responses" {
		t.Errorf("upstream path %s", f.path)
	}
	if up := upstreamBody(t, f); up["max_output_tokens"] != float64(50) {
		t.Errorf("max_output_tokens: %v", up)
	}
	m := decodeMap(t, body)
	cand := candidate(t, m)
	if p := partsOf(t, cand); len(p) != 1 || p[0]["text"] != "Hello" || cand["finishReason"] != "STOP" {
		t.Errorf("reply: %v", cand)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(7) || u["candidatesTokenCount"] != float64(3) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiToAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":4,"output_tokens":1}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hey"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[{"role":"user","parts":[{"text":"hi"}]}],
	  "generationConfig":{"maxOutputTokens":100}}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/messages" {
		t.Errorf("upstream path %s", f.path)
	}
	up := upstreamBody(t, f)
	if up["system"] != "be brief" || up["max_tokens"] != float64(100) {
		t.Errorf("system and max_tokens: %v", up)
	}
	m := decodeMap(t, body)
	cand := candidate(t, m)
	if p := partsOf(t, cand); len(p) != 1 || p[0]["text"] != "Hey" || cand["finishReason"] != "STOP" {
		t.Errorf("reply: %v", cand)
	}
	u := m["usageMetadata"].(map[string]any)
	if u["promptTokenCount"] != float64(4) || u["candidatesTokenCount"] != float64(2) {
		t.Errorf("usage: %v", u)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	direct := `{"contents":[{"role":"user","parts":[{"text":"hello there, count me please"}]}]}`
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", direct)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	m := decodeMap(t, body)
	n, _ := m["totalTokens"].(float64)
	if n <= 0 {
		t.Errorf("totalTokens: %s", body)
	}
	if b, _ := json.Marshal(m); string(b) != body {
		t.Errorf("not compact JSON: %s", body)
	}
	code, wrapped := post(t, "/v1beta/models/fake/m1:countTokens",
		`{"generateContentRequest":{"contents":[{"role":"user","parts":[{"text":"hello there, count me please"}]}]}}`)
	if code != 200 || wrapped != body {
		t.Errorf("wrapped request should count the same: %d %s", code, wrapped)
	}
}

func TestGeminiModelList(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := gemGet(t, "/v1beta/models")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	found := false
	for _, m := range out.Models {
		if m.Name == "models/fake/m1" {
			found = true
			want := "generateContent streamGenerateContent countTokens"
			if got := strings.Join(m.SupportedGenerationMethods, " "); got != want {
				t.Errorf("methods %q, want %q", got, want)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}
}

func TestGeminiUnknownModelIsNotFound(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", `{"contents":[]}`)
	if code != 404 {
		t.Fatalf("status %d: %s", code, body)
	}
	if !strings.HasPrefix(body, `{"error":{"code":404,`) || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("error shape: %s", body)
	}
}

func TestGeminiRootAndNotFoundMentionV1beta(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if _, body := gemGet(t, "/"); !strings.Contains(body, "v1beta") {
		t.Errorf("root info lacks v1beta: %s", body)
	}
	code, body := gemGet(t, "/nothing-here")
	if code != 404 || !strings.Contains(body, "v1beta") {
		t.Errorf("404 hint lacks v1beta: %d %s", code, body)
	}
}

func TestGeminiCallIsRecorded(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	srv := New()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1beta/models/fake/m1:generateContent",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)))
	calls := srv.Recent()
	if len(calls) != 1 || calls[0].From != provider.Gemini || calls[0].Status != 200 || calls[0].Model != "fake/m1" || calls[0].Provider != "fake" {
		t.Errorf("call log: %+v", calls)
	}
}

func TestGeminiThinkingBudgets(t *testing.T) {
	cases := []struct {
		budget   string
		thinking bool
		effort   string
	}{
		{"-1", true, "medium"},
		{"0", false, ""},
		{"2000", true, "low"},
		{"30000", true, "xhigh"},
	}
	for _, c := range cases {
		r, err := parseGemini([]byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"thinkingBudget":` + c.budget + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.Thinking != c.thinking || r.Effort != c.effort {
			t.Errorf("budget %s: thinking %v effort %q, want %v %q", c.budget, r.Thinking, r.Effort, c.thinking, c.effort)
		}
	}
}

func TestGeminiToolChoiceMapping(t *testing.T) {
	decl := `"tools":[{"functionDeclarations":[{"name":"a"},{"name":"b"}]}]`
	cases := map[string]string{
		`{"mode":"NONE"}`:                                  "none",
		`{"mode":"AUTO"}`:                                  "auto",
		`{"mode":"ANY"}`:                                   "required",
		`{"mode":"ANY","allowedFunctionNames":["b","a"]}`:  "required",
		`{"mode":"ANY","allowedFunctionNames":["b"]}`:      "name:b",
	}
	for cfg, want := range cases {
		r, err := parseGemini([]byte(`{"contents":[],` + decl + `,"toolConfig":{"functionCallingConfig":` + cfg + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		if r.ToolChoice != want {
			t.Errorf("%s: got %q want %q", cfg, r.ToolChoice, want)
		}
	}
}

func TestGeminiSchemaTypesLowerCased(t *testing.T) {
	got := string(lowerSchema(json.RawMessage(`{"type":"OBJECT","properties":{"type":{"type":"STRING"},"xs":{"type":"ARRAY","items":{"type":"INTEGER"}}}}`)))
	want := `{"properties":{"type":{"type":"string"},"xs":{"items":{"type":"integer"},"type":"array"}},"type":"object"}`
	if got != want {
		t.Errorf("lowerSchema:\n got %s\nwant %s", got, want)
	}
}

func TestGeminiFunctionCallWithoutIDIsPairedByName(t *testing.T) {
	r, err := parseGemini([]byte(`{"contents":[
	  {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}},{"functionCall":{"id":"keep","name":"write","args":{}}}]},
	  {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"ok"}}},{"functionResponse":{"id":"keep","name":"write","response":{"output":"done"}}}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Messages) != 2 {
		t.Fatalf("messages: %+v", r.Messages)
	}
	calls := r.Messages[0].Parts
	results := r.Messages[1].Parts
	if calls[0].ID == "" || results[0].CallID != calls[0].ID || results[1].CallID != "keep" || calls[1].ID != "keep" {
		t.Errorf("pairing: calls %+v results %+v", calls, results)
	}
}
