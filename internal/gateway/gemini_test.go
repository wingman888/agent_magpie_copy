package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/dial/internal/provider"
)

// getReq calls the gateway with a GET request.
func getReq(t *testing.T, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, s)
	}
	return m
}

func firstCandidate(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	return m["candidates"].([]any)[0].(map[string]any)
}

func candidateParts(t *testing.T, m map[string]any) []any {
	t.Helper()
	return firstCandidate(t, m)["content"].(map[string]any)["parts"].([]any)
}

func TestGeminiGenerateContentFromChat(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c1","model":"m1","choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"content":" there"}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":4,"completion_tokens_details":{"reasoning_tokens":2}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	if f.path != "/v1/chat/completions" {
		t.Errorf("upstream path %s", f.path)
	}
	out := decode(t, body)
	parts := candidateParts(t, out)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "Hello there" {
		t.Errorf("parts: %v", parts)
	}
	if firstCandidate(t, out)["finishReason"] != "STOP" {
		t.Errorf("finishReason: %v", firstCandidate(t, out))
	}
	um := out["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(12) || um["candidatesTokenCount"] != float64(4) || um["thoughtsTokenCount"] != float64(2) {
		t.Errorf("usage: %v", um)
	}
	if !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("reply is not compact: %s", body)
	}
}

func TestGeminiStreamOrderAndToolCall(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c2","model":"m1","choices":[{"delta":{"reasoning_content":"think"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"content":"Answer"}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"read","arguments":""}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.go\"}"}}]}}]}`,
		`data: {"id":"c2","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"id":"c2","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	code, body := post(t, "/v1beta/models/fake/m1:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	evs := events(body)
	if len(evs) != 3 {
		t.Fatalf("want 3 events, got %d: %s", len(evs), body)
	}
	think := candidateParts(t, evs[0])[0].(map[string]any)
	if think["text"] != "think" || think["thought"] != true {
		t.Errorf("thought event: %v", think)
	}
	if candidateParts(t, evs[1])[0].(map[string]any)["text"] != "Answer" {
		t.Errorf("text event: %v", evs[1])
	}
	call := candidateParts(t, evs[2])[0].(map[string]any)["functionCall"].(map[string]any)
	args := call["args"].(map[string]any)
	if call["id"] != "call_9" || call["name"] != "read" || args["path"] != "a.go" {
		t.Errorf("functionCall: %v", call)
	}
	last := evs[2]
	if firstCandidate(t, last)["finishReason"] != "STOP" {
		t.Errorf("last event finishReason: %v", last)
	}
	um := last["usageMetadata"].(map[string]any)
	if um["thoughtsTokenCount"] != float64(3) || um["promptTokenCount"] != float64(10) {
		t.Errorf("last event usage: %v", um)
	}
	for _, ev := range evs[:2] {
		if _, ok := ev["usageMetadata"]; ok {
			t.Errorf("usage on a non-final event: %v", ev)
		}
	}
}

func TestGeminiRequestTranslation(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"id":"c3","model":"m1","choices":[{"delta":{"content":"ok"}}]}`,
		`data: {"id":"c3","choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)}
	setup(t, provider.Chat, f)
	body := `{
	  "systemInstruction":{"parts":[{"text":"be brief"}]},
	  "contents":[
	    {"role":"user","parts":[{"text":"read a.go"}]},
	    {"role":"model","parts":[{"functionCall":{"name":"read","args":{"path":"x"}}}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"read","response":{"output":"package x"}}}]}
	  ],
	  "tools":[{"functionDeclarations":[{"name":"read","description":"read a file",
	    "parameters":{"type":"OBJECT","properties":{"path":{"type":"STRING"}},"required":["path"]}}]}],
	  "generationConfig":{"thinkingConfig":{"thinkingBudget":-1},"maxOutputTokens":100,"temperature":0.5,"topP":0.9,"stopSequences":["END"]}
	}`
	code, reply := post(t, "/v1beta/models/fake/m1:generateContent", body)
	if code != 200 {
		t.Fatalf("status %d: %s", code, reply)
	}
	var up map[string]any
	json.Unmarshal(f.got, &up)
	msgs := up["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("messages: %v", msgs)
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "be brief" {
		t.Errorf("system message: %v", sys)
	}
	asst := msgs[2].(map[string]any)
	calls := asst["tool_calls"].([]any)
	if asst["role"] != "assistant" || len(calls) != 1 {
		t.Fatalf("assistant message: %v", asst)
	}
	callID := calls[0].(map[string]any)["id"].(string)
	if callID == "" {
		t.Errorf("generated call id is empty")
	}
	tool := msgs[3].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != callID {
		t.Errorf("tool message does not pair with the call: %v", tool)
	}
	if up["reasoning_effort"] != "medium" || up["max_tokens"] != float64(100) || up["temperature"] != 0.5 || up["top_p"] != 0.9 {
		t.Errorf("params: %v", up)
	}
	if stop := up["stop"].([]any); len(stop) != 1 || stop[0] != "END" {
		t.Errorf("stop: %v", up["stop"])
	}
	fn := up["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	schema := fn["parameters"].(map[string]any)
	if schema["type"] != "object" || schema["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("schema not lower-cased: %v", schema)
	}
}

func TestGeminiUnknownModel(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: sse(`data: [DONE]`)})
	code, body := post(t, "/v1beta/models/nope/x:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 404 || !strings.Contains(body, `"status":"NOT_FOUND"`) {
		t.Errorf("status %d: %s", code, body)
	}
	if !strings.Contains(body, `"code":404`) {
		t.Errorf("error code missing: %s", body)
	}
}

func TestGeminiCountTokens(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: sse(`data: [DONE]`)})
	text := `{"contents":[{"role":"user","parts":[{"text":"hello world, this is long"}]}]}`
	code, body := post(t, "/v1beta/models/fake/m1:countTokens", text)
	if code != 200 || body != `{"totalTokens":7}` {
		t.Errorf("status %d: %q", code, body)
	}
	code, body = post(t, "/v1beta/models/fake/m1:countTokens", `{"generateContentRequest":`+text+`}`)
	if code != 200 || body != `{"totalTokens":7}` {
		t.Errorf("wrapped: status %d: %q", code, body)
	}
}

func TestGeminiListModels(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: sse(`data: [DONE]`)})
	code, body := getReq(t, "/v1beta/models")
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	models := decode(t, body)["models"].([]any)
	found := false
	for _, m := range models {
		mm := m.(map[string]any)
		if mm["name"] == "models/fake/m1" {
			found = true
			if len(mm["supportedGenerationMethods"].([]any)) == 0 {
				t.Errorf("no supportedGenerationMethods: %v", mm)
			}
		}
	}
	if !found {
		t.Errorf("models/fake/m1 not listed: %s", body)
	}
}

func TestGeminiInfoAndNotFoundMentionV1beta(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t, reply: sse(`data: [DONE]`)})
	if _, body := getReq(t, "/"); !strings.Contains(body, "v1beta") {
		t.Errorf("info lacks v1beta: %s", body)
	}
	if _, body := post(t, "/nope", `{}`); !strings.Contains(body, "v1beta") {
		t.Errorf("404 hint lacks v1beta: %s", body)
	}
}

func TestGeminiFromAnthropicUpstream(t *testing.T) {
	f := &fake{t: t, reply: sse(
		`data: {"type":"message_start","message":{"id":"msg_1","model":"m1","usage":{"input_tokens":7,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		`data: {"type":"message_stop"}`)}
	setup(t, provider.Anthropic, f)
	code, body := post(t, "/v1beta/models/fake/m1:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	out := decode(t, body)
	if candidateParts(t, out)[0].(map[string]any)["text"] != "Hi" || firstCandidate(t, out)["finishReason"] != "STOP" {
		t.Errorf("reply: %s", body)
	}
	um := out["usageMetadata"].(map[string]any)
	if um["promptTokenCount"] != float64(7) || um["candidatesTokenCount"] != float64(3) {
		t.Errorf("usage: %v", um)
	}
}

func TestParseGeminiOptions(t *testing.T) {
	r, err := parseGemini([]byte(`{
	  "contents":[
	    {"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"AAA"}},{"inlineData":{"mimeType":"audio/wav","data":"BBB"}}]},
	    {"role":"model","parts":[{"functionCall":{"name":"f","args":{"a":1}}},{"text":"hidden","thought":true}]},
	    {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"x":1}}}]}
	  ],
	  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}},
	  "generationConfig":{"responseMimeType":"application/json","responseSchema":{"type":"OBJECT","properties":{"n":{"type":"INTEGER"}}},"thinkingConfig":{"thinkingBudget":0}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Messages[0].Parts[0].Kind != Image || r.Messages[0].Parts[0].Data != "AAA" {
		t.Errorf("image: %+v", r.Messages[0].Parts[0])
	}
	if r.Messages[0].Parts[1].Kind != Text || r.Messages[0].Parts[1].Text != "[attachment: audio/wav]" {
		t.Errorf("attachment placeholder: %+v", r.Messages[0].Parts[1])
	}
	call := r.Messages[1].Parts[0]
	if call.Kind != ToolCall || call.ID == "" {
		t.Fatalf("call: %+v", call)
	}
	res := r.Messages[2].Parts[0]
	if res.Kind != ToolResult || res.CallID != call.ID {
		t.Errorf("response does not pair with call: %+v vs %+v", res, call)
	}
	if r.Messages[1].Parts[1].Kind != Thinking {
		t.Errorf("thought part: %+v", r.Messages[1].Parts[1])
	}
	if r.ToolChoice != "name:f" {
		t.Errorf("tool choice %q", r.ToolChoice)
	}
	if !strings.Contains(r.System, "JSON") || !strings.Contains(r.System, `"type":"object"`) {
		t.Errorf("json instruction: %q", r.System)
	}
	if r.Effort != "" || r.Thinking {
		t.Errorf("budget 0 should turn thinking off: effort %q thinking %v", r.Effort, r.Thinking)
	}
}
